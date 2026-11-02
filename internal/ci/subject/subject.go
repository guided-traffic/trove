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
//	issuer := lookup(u.Issuer)                   // step 5: unknown_issuer, issuer_disabled (the caller)
//	v, err := u.Verify(ctx, settings, keys, now) // steps 3 (alg, typ), 6, 7, 8, 9
//
// Reason precedence follows from that split: a token of an unknown issuer answers unknown_issuer
// even when its algorithm would be refused by every issuer, because the algorithm list is an issuer
// setting (Q-CI-19). Both are 400 invalid_request (R-CI-008).
//
// The JWS and JWK code is shared with the login verifier, not copied (Q-CI-19): work package W2 moves
// internal/auth/oidc/jws.go into the leaf internal/auth/jws, extends it with EdDSA, the 2048-bit RSA
// minimum and the `key_ops` rule, and both internal/auth/oidc and the key store of the CI issuers use
// it. This package depends only on the key abstraction [Key].
//
// The bodies are stubs until W2 builds them.
package subject

import (
	"context"
	"crypto"
	"errors"
	"time"

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
// type matches the algorithm family).
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
	Keys(ctx context.Context, kid, alg string) ([]Key, error)
}

// MaxKeysWithoutKID is the number of keys tried for a token without a kid (R-CI-004 step 6).
const MaxKeysWithoutKID = 10

// MaxKIDLength is the longest accepted kid (R-CI-004 step 3).
const MaxKIDLength = 256

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
	// Header is the parsed, unverified header. The token text itself is kept by W2's private fields
	// and is never exposed, logged or put in an error.
	Header Header
}

// Parse runs the structural steps of R-CI-004 and stops at the first failure: size at most
// maxBytes (subject_token_malformed); compact JWS of three base64url parts, JWE, unsecured and nested
// JWTs refused (subject_token_malformed), a value that starts with `glcbt-` is
// wrong_subject_token_type; a `crit` parameter and a kid over MaxKIDLength (subject_token_malformed);
// a payload that is a UTF-8 JSON object without duplicate member names (subject_token_malformed).
func Parse(_ string, _ int) (Unverified, error) {
	panic("not implemented: W2")
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
// never in it), typ in AllowedTyp or absent with AllowMissingTyp (token_type_not_allowed); the
// signature against keys.Keys (signature_invalid, unknown_signing_key; ErrKeysUnavailable is
// returned as is, so the caller answers 503); the audience against AllowedAudiences, exclusively
// when ExclusiveAudience (audience_invalid, audience_not_exclusive); exp, nbf and iat with
// ClockSkew (token_expired, token_not_yet_valid, issued_in_future; NumericDate may be fractional);
// every RequiredClaims member present and not null (required_claim_missing, Detail names the claim).
//
// A failure is a *Failure. The exchange calls Verify a second time against a re-read issuer row
// when the issuer's version changed under it (R-CI-007), with the same keys.
func (u Unverified) Verify(_ context.Context, _ Settings, _ KeySource, _ time.Time) (Verified, error) {
	panic("not implemented: W2")
}
