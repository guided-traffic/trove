// Package subject validates the subject token of the CI exchange (R-CI-004): the ID token a job
// presents. It is pure: no database, no network, no clock but the one it is given. The caller owns
// everything that reads state, the selection of the CI issuer by the unverified `iss`
// (R-CI-004 step 5, R-CI-005) and the key lookup with its re-read and refetch of an unknown `kid`
// (R-CI-003), which this package reaches through [KeySource].
//
// The validation is two calls, because step 5 selects the issuer by a claim that is not yet verified
// and the settings of steps 3 and 6 to 9 are that issuer's:
//
//	u, err := subject.Parse(raw, max)           // steps 1, 2, the header's shape, 4
//	issuer, found := lookup(u.Issuer)            // step 5 (the caller) ...
//	err = subject.CheckIssuer(found, enabled)    // ... unknown_issuer, issuer_disabled
//	v, err := u.Verify(ctx, settings, keys, now) // steps 3 (alg, typ), 6, 7, 8, 9
//
// Reason precedence follows from that split: a token of an unknown issuer answers unknown_issuer
// even when its algorithm would be refused by every issuer, because the algorithm list is an issuer
// setting (Q-CI-19). Both are 400 invalid_request (R-CI-008).
//
// The JWS and JWK code is shared with the login verifier, not copied (Q-CI-19): this package parses
// and verifies with the leaf internal/auth/jws and depends otherwise only on the key abstraction
// [Key], which the key store of the CI issuers builds from its stored JWK set.
//
// Every Detail is a fixed text or names a claim of the issuer's settings: none holds a claim value
// or a fragment of the token (R-CI-008), so the exchange may log and audit it.
package subject

import (
	"context"
	"crypto"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/auth/jws"
	"github.com/guided-traffic/trove/internal/ci/claims"
	"github.com/guided-traffic/trove/internal/ci/reason"
)

// Failure is a refusal of the validation with the reason of R-CI-004's brackets. Detail may name a
// claim (required_claim_missing) and never holds a claim value or a token fragment (R-CI-008).
type Failure struct {
	Reason reason.Reason
	Detail string
}

// Error implements error.
func (f *Failure) Error() string { return string(f.Reason) + ": " + f.Detail }

func fail(r reason.Reason, detail string) *Failure { return &Failure{Reason: r, Detail: detail} }

// The sentinel errors a [KeySource] returns; Verify maps them to unknown_signing_key and
// signing_keys_unavailable.
var (
	// ErrUnknownKey: the kid is not in the issuer's key set, after the caller's re-read of the row
	// and, when its throttle allowed it, its fetch (R-CI-003 steps 1 to 3).
	ErrUnknownKey = errors.New("subject: unknown signing key")
	// ErrKeysUnavailable: the stored key set is older than ci.signing_keys.max_stale, or there is
	// none and none could be fetched (R-CI-003 "Fetch failures"). The exchange answers 503
	// signing_keys_unavailable, not 400.
	ErrKeysUnavailable = errors.New("subject: signing keys unavailable")
)

// Key is one usable verification key of a CI issuer. The key store (W1) builds it from the stored
// JWKS and has already applied the key rules of R-CI-003 (use, key_ops, kty, 2048-bit RSA); Verify
// applies the rules that depend on the token (the key's own alg equals the token's alg, the key
// type matches the algorithm family) and checks the type and size again.
//
// A key of [jws.ParseJWKS] converts field by field: Key{KID: k.KID, KTY: k.KTY, Alg: k.Alg,
// Public: k.Public}.
type Key struct {
	// KID may be empty for a key set without kids.
	KID string
	// KTY is RSA, EC or OKP.
	KTY string
	// Alg is the key's own alg member, "" when absent.
	Alg string
	// Public is *rsa.PublicKey, *ecdsa.PublicKey or ed25519.PublicKey.
	Public crypto.PublicKey
}

// KeySource looks up the keys that may verify a token. The caller implements it over its in-process
// copy of the issuer's key row; the unknown-kid protocol of R-CI-003 (re-read at most once per
// issuer, replica and poll interval, then a throttled fetch) is inside it, so this package stays
// free of I/O.
type KeySource interface {
	// Keys returns the keys for a token. With a kid: the key of that name, or ErrUnknownKey. Without a
	// kid: every usable key whose key type fits alg, at most MaxKeysWithoutKID, and an issuer without a
	// stored key set counts as an unknown kid (R-CI-003). ErrKeysUnavailable fails closed.
	//
	// A key the source returns that does not fit alg (its type, curve, size or own alg) is ignored by
	// Verify like a key that is not there: when no returned key fits, the answer is
	// unknown_signing_key, so a source that wants the refetch of R-CI-003 for such a key applies the
	// fit itself (jws.Fits) before it answers.
	Keys(ctx context.Context, kid, alg string) ([]Key, error)
}

// MaxKeysWithoutKID is the number of keys tried for a token without a kid (R-CI-004 step 6). Verify
// tries at most this many keys for a token with a kid too.
const MaxKeysWithoutKID = 10

// MaxKIDLength is the longest accepted kid (R-CI-004 step 3).
const MaxKIDLength = jws.MaxKIDLength

// DefaultMaxBytes is the default of ci.exchange.max_subject_token_bytes; Parse applies it when the
// caller passes no positive limit, so a zero value never means "unbounded".
const DefaultMaxBytes = 16384

// JobTokenPrefix starts every GitLab job token (`CI_JOB_TOKEN`) of the legacy format.
const JobTokenPrefix = "glcbt-"

// Header is the protected header of a verified token. Only these members are read; `jku`, `x5u`,
// `jwk` and `x5c` are ignored (RFC 8725 §3.10, R-CI-003), `crit` is refused.
type Header struct {
	Alg string
	Typ string
	KID string
}

// Unverified is a token that passed the structural steps and is not yet trusted: only Issuer may be
// used, to select the CI issuer, and the audit event of a denial may log Issuer and KID and nothing
// else of it (R-CI-020).
type Unverified struct {
	// Issuer is the unverified `iss`, "" when the payload has none or a non-string.
	Issuer string
	// KID is the header's kid, "" when absent.
	KID string
	// Header is the parsed, unverified header. The token text itself is kept in private fields and is
	// never exposed, logged or put in an error.
	Header Header

	token      *jws.Token
	payload    map[string]any
	typPresent bool
}

// Parse runs the structural steps of R-CI-004 and stops at the first failure: size at most
// maxBytes (subject_token_malformed; a maxBytes of zero or less is DefaultMaxBytes); a value that
// starts with `glcbt-` is wrong_subject_token_type; compact JWS of three base64url parts, JWE,
// unsecured (`alg` none or an empty signature) and nested (`cty` JWT) JWTs refused
// (subject_token_malformed); a `crit` parameter, a header with duplicate member names, a missing
// `alg` and a kid over MaxKIDLength (subject_token_malformed); a payload that is a UTF-8 JSON object
// without duplicate member names at any level (subject_token_malformed).
//
// Of the payload only `iss` is read, into Issuer (step 5); the rest waits for the signature.
func Parse(raw string, maxBytes int) (Unverified, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	if len(raw) > maxBytes {
		return Unverified{}, fail(reason.SubjectTokenMalformed, "the subject token is longer than ci.exchange.max_subject_token_bytes ("+strconv.Itoa(maxBytes)+" bytes)")
	}
	if strings.HasPrefix(raw, JobTokenPrefix) {
		return Unverified{}, fail(reason.WrongSubjectTokenType, "the subject token is a GitLab job token (CI_JOB_TOKEN); request an ID token with id_tokens: and send that")
	}
	tok, err := jws.Parse(raw)
	if err != nil {
		return Unverified{}, fail(reason.SubjectTokenMalformed, "the subject token is not a compact JWS: "+jws.Detail(err))
	}
	if tok.Unsecured() {
		return Unverified{}, fail(reason.SubjectTokenMalformed, "the subject token is an unsecured JWT (alg none or no signature)")
	}
	if strings.EqualFold(tok.Header.Cty, "JWT") {
		return Unverified{}, fail(reason.SubjectTokenMalformed, "the subject token is a nested JWT (cty JWT), which is not supported")
	}
	payload, err := jws.DecodeObject(tok.Payload)
	if err != nil {
		return Unverified{}, fail(reason.SubjectTokenMalformed, "the payload is not a UTF-8 JSON object without duplicate member names: "+jws.Detail(err))
	}
	iss, _ := payload["iss"].(string)
	return Unverified{
		Issuer: iss,
		KID:    tok.Header.Kid,
		Header: Header{Alg: tok.Header.Alg, Typ: tok.Header.Typ, KID: tok.Header.Kid},

		token:      tok,
		payload:    payload,
		typPresent: tok.Header.HasTyp,
	}, nil
}

// CheckIssuer is step 5 of R-CI-004 for the caller, which looked up the CI issuer whose `issuer`
// equals Unverified.Issuer exactly (R-CI-005): none found is unknown_issuer, a disabled one
// issuer_disabled, otherwise nil. Nothing else of the unverified payload is used before Verify.
func CheckIssuer(found, enabled bool) error {
	switch {
	case !found:
		return fail(reason.UnknownIssuer, "no CI issuer has the token's iss")
	case !enabled:
		return fail(reason.IssuerDisabled, "the CI issuer of the token's iss is disabled")
	}
	return nil
}

// Settings are the validation settings of one CI issuer (R-CI-001): the fields of the issuer record
// that steps 3 and 6 to 9 read.
type Settings struct {
	Issuer            string
	AllowedAudiences  []string
	ExclusiveAudience bool
	AllowedAlgorithms []string
	AllowedTyp        []string
	AllowMissingTyp   bool
	ClockSkew         time.Duration
	RequiredClaims    []string
}

// Verified is a token that passed every step. Claims are the verified claims, for the claim engine
// and the issuance record; they are the only claims the exchange may use.
type Verified struct {
	Header Header
	Claims claims.Claims
	// JTI is the `jti` claim, "" when absent; single use rejects an empty one (R-CI-018).
	JTI string
	// IssuedAt, ExpiresAt are `iat` and `exp`; NotBefore is the zero time when `nbf` is absent.
	IssuedAt, ExpiresAt, NotBefore time.Time
}

// Verify runs the remaining steps for the issuer the caller selected, in the order of R-CI-004, and
// stops at the first failure: alg in AllowedAlgorithms (algorithm_not_allowed; `none` and HMAC are
// never in it), typ in AllowedTyp, compared case-insensitively, or absent with AllowMissingTyp
// (token_type_not_allowed); the signature against keys.Keys (signature_invalid,
// unknown_signing_key; ErrKeysUnavailable and every other error of the source are returned as is, so
// the caller answers 503); the audience against AllowedAudiences, exclusively when
// ExclusiveAudience (audience_invalid, audience_not_exclusive); exp, nbf and iat with ClockSkew
// (token_expired, token_not_yet_valid, issued_in_future; NumericDate may be fractional; a missing or
// null exp or iat is required_claim_missing and a time claim that is not a NumericDate
// subject_token_malformed, Q-CI-27); every RequiredClaims member present and not null
// (required_claim_missing, Detail names the claim).
//
// A failure is a *Failure. The exchange calls Verify a second time against a re-read issuer row
// when the issuer's version changed under it (R-CI-007), with the same keys.
func (u Unverified) Verify(ctx context.Context, s Settings, keys KeySource, now time.Time) (Verified, error) {
	if u.token == nil || u.payload == nil {
		return Verified{}, fail(reason.SubjectTokenMalformed, "the subject token was not parsed")
	}
	if err := u.checkHeader(s); err != nil {
		return Verified{}, err
	}
	if err := u.checkSignature(ctx, keys); err != nil {
		return Verified{}, err
	}
	// From here on the payload is verified.
	if iss, _ := u.payload["iss"].(string); s.Issuer != "" && iss != s.Issuer {
		return Verified{}, fail(reason.UnknownIssuer, "the verified iss is not the issuer of these settings")
	}
	if err := checkAudience(u.payload["aud"], s); err != nil {
		return Verified{}, err
	}
	times, err := checkTimes(u.payload, s.ClockSkew, now)
	if err != nil {
		return Verified{}, err
	}
	for _, name := range s.RequiredClaims {
		if v, ok := u.payload[name]; !ok || v == nil {
			return Verified{}, fail(reason.RequiredClaimMissing, "the required claim "+name+" is missing or null")
		}
	}
	c := make(claims.Claims, len(u.payload))
	for k, v := range u.payload {
		c[k] = v
	}
	jti, _ := u.payload["jti"].(string)
	return Verified{
		Header: u.Header, Claims: c, JTI: jti,
		IssuedAt: times.iat, ExpiresAt: times.exp, NotBefore: times.nbf,
	}, nil
}

// checkHeader is the part of step 3 that needs the issuer: the algorithm and the typ.
func (u Unverified) checkHeader(s Settings) error {
	alg := u.token.Header.Alg
	allowed := false
	for _, a := range s.AllowedAlgorithms {
		if a == alg {
			allowed = true
			break
		}
	}
	if !allowed || !jws.Supported(alg) {
		return fail(reason.AlgorithmNotAllowed, "the token's alg is not one of the issuer's allowed_algorithms (none and HMAC never are)")
	}
	if !u.typPresent {
		if !s.AllowMissingTyp {
			return fail(reason.TokenTypeNotAllowed, "the token has no typ header and the issuer does not allow_missing_typ")
		}
		return nil
	}
	for _, t := range s.AllowedTyp {
		if strings.EqualFold(t, u.token.Header.Typ) {
			return nil
		}
	}
	return fail(reason.TokenTypeNotAllowed, "the token's typ is not one of the issuer's allowed_typ")
}

// checkSignature is step 6: the keys of the kid, or without kid at most MaxKeysWithoutKID keys of
// the algorithm's type, each tried once. A key that does not fit the algorithm is not a candidate.
func (u Unverified) checkSignature(ctx context.Context, keys KeySource) error {
	if keys == nil {
		return fail(reason.UnknownSigningKey, "the issuer has no key source")
	}
	alg := u.token.Header.Alg
	ks, err := keys.Keys(ctx, u.KID, alg)
	if errors.Is(err, ErrUnknownKey) {
		return fail(reason.UnknownSigningKey, "no key of the issuer has the token's kid, after a re-read and a refetch")
	}
	if err != nil {
		return err
	}
	tried := 0
	for _, k := range ks {
		if tried == MaxKeysWithoutKID {
			break
		}
		if u.KID != "" && k.KID != u.KID {
			continue
		}
		if !jws.Fits(alg, k.Public, k.Alg) {
			continue
		}
		tried++
		if u.token.VerifyWith(k.Public, k.Alg) == nil {
			return nil
		}
	}
	if tried == 0 {
		return fail(reason.UnknownSigningKey, "no key of the issuer fits the token's kid and alg")
	}
	return fail(reason.SignatureInvalid, "the signature does not verify with the issuer's key")
}

// checkAudience is step 7 (RFC 8725 §3.9): aud is a string or a non-empty array of strings; one
// element must be allowed, and with ExclusiveAudience every element.
func checkAudience(v any, s Settings) error {
	var aud []string
	switch t := v.(type) {
	case string:
		aud = []string{t}
	case []any:
		for _, el := range t {
			str, ok := el.(string)
			if !ok {
				return fail(reason.AudienceInvalid, "the token's aud holds a value that is not a string")
			}
			aud = append(aud, str)
		}
	}
	allowed := func(a string) bool {
		for _, x := range s.AllowedAudiences {
			if x == a {
				return true
			}
		}
		return false
	}
	some := false
	for _, a := range aud {
		some = some || allowed(a)
	}
	if !some {
		return fail(reason.AudienceInvalid, "no element of the token's aud is one of the issuer's allowed_audiences")
	}
	if s.ExclusiveAudience {
		for _, a := range aud {
			if !allowed(a) {
				return fail(reason.AudienceNotExclusive, "the token's aud also names a recipient that is not one of the issuer's allowed_audiences")
			}
		}
	}
	return nil
}

type tokenTimes struct{ exp, nbf, iat time.Time }

// checkTimes is step 8 (RFC 7519 §4.1.4 to §4.1.6) with the skew.
func checkTimes(p map[string]any, skew time.Duration, now time.Time) (tokenTimes, error) {
	var t tokenTimes
	var err error
	if t.exp, err = numericDate(p, "exp", true); err != nil {
		return t, err
	}
	if !now.Before(t.exp.Add(skew)) {
		return t, fail(reason.TokenExpired, "the token expired (exp plus the clock skew has passed)")
	}
	if t.nbf, err = numericDate(p, "nbf", false); err != nil {
		return t, err
	}
	if !t.nbf.IsZero() && now.Before(t.nbf.Add(-skew)) {
		return t, fail(reason.TokenNotYetValid, "the token is not valid yet (nbf minus the clock skew is ahead)")
	}
	if t.iat, err = numericDate(p, "iat", true); err != nil {
		return t, err
	}
	if t.iat.After(now.Add(skew)) {
		return t, fail(reason.IssuedInFuture, "the token was issued in the future (iat is ahead by more than the clock skew)")
	}
	return t, nil
}

// maxNumericDate bounds a NumericDate to a time the standard library represents (the year 36812),
// as the login does.
const maxNumericDate = 1 << 40

// numericDate reads a NumericDate claim (RFC 7519 §2): seconds since the epoch, possibly
// fractional. A required claim that is missing or null is required_claim_missing; an optional one
// that is missing or null is the zero time; a value that is not a non-negative number within
// maxNumericDate is subject_token_malformed (Q-CI-27).
func numericDate(p map[string]any, name string, required bool) (time.Time, error) {
	v, ok := p[name]
	if !ok || v == nil {
		if required {
			return time.Time{}, fail(reason.RequiredClaimMissing, "the required claim "+name+" is missing or null")
		}
		return time.Time{}, nil
	}
	n, isNumber := v.(json.Number)
	if !isNumber {
		return time.Time{}, fail(reason.SubjectTokenMalformed, "the claim "+name+" is not a NumericDate")
	}
	f, err := strconv.ParseFloat(string(n), 64)
	if err != nil || f < 0 || f > maxNumericDate {
		return time.Time{}, fail(reason.SubjectTokenMalformed, "the claim "+name+" is not a NumericDate within range")
	}
	sec := int64(f)
	nsec := int64((f - float64(sec)) * 1e9)
	return time.Unix(sec, nsec), nil
}
