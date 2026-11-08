package registrytoken

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/secrets"
)

// The form of a registry token (R-OCI-005, ADR-163):
//
//	payload = "1" ":" <key id> ":" <claims as JSON>
//	signed  = "trv_rgt_" + base64url(payload)                 // RFC 4648 §5, no padding
//	token   = signed + "." + base64url(HMAC-SHA-256(key, signed))
//
// The key ID stands before the claims, so the verifier finds the key and checks the MAC before it parses anything the
// client sent; the MAC covers the exact characters the client presents, so no canonical form of the claims is needed.
const (
	// Prefix starts every registry token. It is not one of the prefixes of R-AUTH-022, so the scanner pattern there does not
	// match a registry token and the syntax step of a trove token refuses it.
	Prefix = "trv_rgt_" //nolint:gosec // G101: a prefix, not a credential
	// MaxLength bounds a registry token; a longer value is refused before anything is decoded.
	MaxLength = 4096
	// MaxEntries bounds the entries of a grant and the scope entries a token request may carry (R-OCI-004).
	MaxEntries = 16
	// MaxLifetime is the upper bound of oci.token_lifetime (R-OCI-005); a [Signer] takes the configured value.
	MaxLifetime = time.Hour
	// MaxClockSkew is how far a token's issue time may lie ahead of the verifying replica's clock: replicas' clocks differ
	// by less, and a token issued further in the future was not issued by a replica.
	MaxClockSkew = time.Minute
	// KeyInfo is the HKDF info of the MAC keys (R-ARCH-021).
	KeyInfo = "trove registry token v1"

	payloadVersion = "1"
	keySize        = sha256.Size
	macChars       = 43 // base64url of 32 bytes without padding
)

// The verification errors. None of them names the reason to the client: the stage answers each with 401 invalid_token
// (R-AUTH-002); the reason goes to the metric of the stage.
var (
	ErrMalformed  = errors.New("registrytoken: malformed")
	ErrUnknownKey = errors.New("registrytoken: signed with a key the ring does not hold")
	ErrSignature  = errors.New("registrytoken: the signature does not verify")
	ErrExpired    = errors.New("registrytoken: expired")
	// ErrLifetime marks claims issued in the future or spanning more than the configured lifetime: only a holder of the key
	// ring can sign them (H-579).
	ErrLifetime = errors.New("registrytoken: issued outside the configured lifetime")
	ErrEndpoint = errors.New("registrytoken: issued on another endpoint")
	// ErrTooLarge is the refusal of [Signer.Issue] for claims that do not fit in [MaxLength].
	ErrTooLarge = errors.New("registrytoken: the grant does not fit in one token")
)

// Is reports whether raw has the prefix of a registry token. It decides nothing about validity.
func Is(raw string) bool { return strings.HasPrefix(raw, Prefix) }

// Binding names the trove token a registry token was issued for (R-OCI-005): its ID for the request log and the audit
// actor, its type for the endpoint's token types, and the SHA-256 of its secret, the key of the token cache of R-AUTH-023.
// The hash allows no guess of the secret (256 bits of entropy, R-AUTH-023); it lets the verifier ask the cache without a
// second index, and a holder of the key ring cannot mint a token for a live trove token without having read it (H-579).
type Binding struct {
	TokenID      string
	TokenType    string
	SecretSHA256 [32]byte
}

// Claims are the content of a registry token (R-OCI-005).
type Claims struct {
	// KeyID is the ID of the ring's key whose derivation signed the token; set by [Signer.Issue].
	KeyID string
	// Anonymous is a token issued to the anonymous principal: it has no principal ID and no binding.
	Anonymous bool
	// PrincipalKind and PrincipalID name the principal the token was issued to.
	PrincipalKind string
	PrincipalID   string
	Binding       Binding
	// Endpoint is the name of the endpoint the token request arrived on (R-EP-002); the token is refused on another.
	Endpoint  string
	IssuedAt  time.Time
	ExpiresAt time.Time
	Grant     Grant
}

// wire is the JSON of the claims. The names are short because the token travels in every request.
type wire struct {
	Anonymous bool        `json:"a,omitempty"`
	Kind      string      `json:"pk,omitempty"`
	Principal string      `json:"p,omitempty"`
	TokenID   string      `json:"t,omitempty"`
	TokenType string      `json:"tt,omitempty"`
	TokenHash string      `json:"th,omitempty"`
	Endpoint  string      `json:"e"`
	IssuedAt  int64       `json:"iat"`
	ExpiresAt int64       `json:"exp"`
	Grant     []wireEntry `json:"g,omitempty"`
	Catalog   bool        `json:"c,omitempty"`
}

type wireEntry struct {
	Repo    string  `json:"r"`
	Name    string  `json:"n"`
	Actions Actions `json:"a"`
}

// bindable reports whether a registry token may be bound to a trove token of the type: the types of R-AUTH-022 that
// authenticate a request on the client listener. A Bastion token never does (R-BAS-005).
func bindable(typ string) bool { return typ == "pat" || typ == "sat" || typ == "cit" }

// Signer issues and verifies registry tokens with the MAC keys derived from the key ring and the configured lifetime. It is
// immutable and safe for concurrent use.
type Signer struct {
	keys     *secrets.DerivedKeys
	lifetime time.Duration
}

// NewSigner derives the MAC keys from every key of the ring (R-ARCH-021, info [KeyInfo]): the active key's derivation
// signs, every key's derivation verifies, so a rotation of the ring needs no new secret and a token signed before it is
// accepted until it expires, as long as its key stays in the ring. lifetime is oci.token_lifetime: no token is issued or
// accepted whose claims span more, so even claims signed with the ring verify for at most that long (H-579).
func NewSigner(ring *secrets.KeyRing, lifetime time.Duration) (*Signer, error) {
	if ring == nil {
		return nil, errors.New("registrytoken: no key ring")
	}
	if lifetime < time.Second || lifetime > MaxLifetime {
		return nil, fmt.Errorf("registrytoken: the lifetime %s is not between 1 s and %s", lifetime, MaxLifetime)
	}
	keys, err := ring.Derive(KeyInfo, keySize)
	if err != nil {
		return nil, err
	}
	return &Signer{keys: keys, lifetime: lifetime}, nil
}

// Lifetime is the configured lifetime of a registry token (oci.token_lifetime).
func (s *Signer) Lifetime() time.Duration { return s.lifetime }

// Issue signs the claims with the active key and returns the token. KeyID is set from the ring. It fails with
// [ErrTooLarge] when the token would exceed [MaxLength], and with an error for claims that [Signer.Verify] would refuse.
func (s *Signer) Issue(c Claims) (string, error) {
	kid, key := s.keys.Active()
	c.KeyID = kid
	if err := c.check(); err != nil {
		return "", err
	}
	if c.ExpiresAt.Sub(c.IssuedAt) > s.lifetime {
		return "", fmt.Errorf("%w: %s", ErrLifetime, c.ExpiresAt.Sub(c.IssuedAt))
	}
	w := wire{
		Anonymous: c.Anonymous, Kind: c.PrincipalKind, Principal: c.PrincipalID, Endpoint: c.Endpoint,
		IssuedAt: c.IssuedAt.Unix(), ExpiresAt: c.ExpiresAt.Unix(), Catalog: c.Grant.Catalog,
	}
	if !c.Anonymous {
		w.TokenID, w.TokenType = c.Binding.TokenID, c.Binding.TokenType
		w.TokenHash = base64.RawURLEncoding.EncodeToString(c.Binding.SecretSHA256[:])
	}
	for _, e := range c.Grant.Entries {
		w.Grant = append(w.Grant, wireEntry{Repo: e.Repo.String(), Name: e.Name, Actions: e.Actions})
	}
	body, err := json.Marshal(w)
	if err != nil {
		return "", fmt.Errorf("registrytoken: %w", err)
	}
	payload := payloadVersion + ":" + kid + ":" + string(body)
	signed := Prefix + base64.RawURLEncoding.EncodeToString([]byte(payload))
	tok := signed + "." + base64.RawURLEncoding.EncodeToString(mac(key, signed))
	if len(tok) > MaxLength {
		return "", ErrTooLarge
	}
	return tok, nil
}

// Verify checks a presented registry token without any lookup (R-OCI-005): the length, the form, the named key, the MAC,
// then the claims, the expiry at now, the issue time (at most [MaxClockSkew] ahead of now), the span against the configured
// lifetime and the endpoint the request arrived on. What it returns is authentic; the state of
// the bound trove token is the stage's to check.
func (s *Signer) Verify(raw, endpoint string, now time.Time) (Claims, error) {
	if len(raw) > MaxLength || !Is(raw) {
		return Claims{}, ErrMalformed
	}
	encoded, sig, ok := strings.Cut(raw[len(Prefix):], ".")
	if !ok || len(sig) != macChars || strings.Contains(sig, ".") {
		return Claims{}, ErrMalformed
	}
	got, err := base64.RawURLEncoding.Strict().DecodeString(sig)
	if err != nil {
		return Claims{}, ErrMalformed
	}
	payload, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return Claims{}, ErrMalformed
	}
	ver, rest, ok1 := bytes.Cut(payload, []byte(":"))
	kid, body, ok2 := bytes.Cut(rest, []byte(":"))
	if !ok1 || !ok2 || string(ver) != payloadVersion || len(kid) == 0 {
		return Claims{}, ErrMalformed
	}
	key, known := s.keys.Key(string(kid))
	if !known {
		return Claims{}, ErrUnknownKey
	}
	if !hmac.Equal(got, mac(key, raw[:len(Prefix)+len(encoded)])) {
		return Claims{}, ErrSignature
	}
	c, err := decode(string(kid), body)
	if err != nil {
		return Claims{}, err
	}
	if !now.Before(c.ExpiresAt) {
		return Claims{}, ErrExpired
	}
	if c.IssuedAt.After(now.Add(MaxClockSkew)) || c.ExpiresAt.Sub(c.IssuedAt) > s.lifetime {
		return Claims{}, ErrLifetime
	}
	if c.Endpoint != endpoint {
		return Claims{}, ErrEndpoint
	}
	return c, nil
}

func mac(key []byte, signed string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(signed))
	return h.Sum(nil)
}

// decode parses the authenticated claims strictly: an unknown member, trailing data or a value outside the rules is
// malformed. A token that passed the MAC was issued by a ring holder, so a failure here is a defect or a forgery with the
// ring (H-579), never a client mistake.
func decode(kid string, body []byte) (Claims, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var w wire
	if err := dec.Decode(&w); err != nil || dec.More() {
		return Claims{}, ErrMalformed
	}
	c := Claims{
		KeyID: kid, Anonymous: w.Anonymous, PrincipalKind: w.Kind, PrincipalID: w.Principal, Endpoint: w.Endpoint,
		IssuedAt: time.Unix(w.IssuedAt, 0), ExpiresAt: time.Unix(w.ExpiresAt, 0),
		Binding: Binding{TokenID: w.TokenID, TokenType: w.TokenType}, Grant: Grant{Catalog: w.Catalog},
	}
	if w.TokenHash != "" {
		h, err := base64.RawURLEncoding.Strict().DecodeString(w.TokenHash)
		if err != nil || len(h) != len(c.Binding.SecretSHA256) {
			return Claims{}, ErrMalformed
		}
		copy(c.Binding.SecretSHA256[:], h)
	}
	for _, e := range w.Grant {
		repo, err := qname.ParseRepo(e.Repo)
		if err != nil {
			return Claims{}, ErrMalformed
		}
		c.Grant.Entries = append(c.Grant.Entries, Entry{Repo: repo, Name: e.Name, Actions: e.Actions})
	}
	if err := c.check(); err != nil {
		return Claims{}, ErrMalformed
	}
	return c, nil
}

// check holds the rules both sides apply to the claims.
func (c Claims) check() error {
	switch {
	case c.KeyID == "":
		return fmt.Errorf("%w: no key ID", ErrMalformed)
	case c.Endpoint == "":
		return fmt.Errorf("%w: no endpoint", ErrMalformed)
	case !c.ExpiresAt.After(c.IssuedAt), c.ExpiresAt.Sub(c.IssuedAt) > MaxLifetime:
		return fmt.Errorf("%w: the lifetime is not between 1 s and %s", ErrMalformed, MaxLifetime)
	case len(c.Grant.Entries) > MaxEntries:
		return fmt.Errorf("%w: more than %d grant entries", ErrMalformed, MaxEntries)
	}
	if c.Anonymous {
		if c.PrincipalID != "" || c.Binding != (Binding{}) {
			return fmt.Errorf("%w: an anonymous token names a principal or a token", ErrMalformed)
		}
	} else if c.PrincipalID == "" || c.PrincipalKind == "" || c.Binding.TokenID == "" || !bindable(c.Binding.TokenType) ||
		c.Binding.SecretSHA256 == [32]byte{} {
		return fmt.Errorf("%w: a token of a principal needs the principal and its trove token", ErrMalformed)
	}
	for _, e := range c.Grant.Entries {
		if !e.Repo.Valid() || e.Name == "" || e.Actions == 0 || e.Actions&^All != 0 {
			return fmt.Errorf("%w: grant entry %s %q", ErrMalformed, e.Repo, e.Name)
		}
	}
	return nil
}
