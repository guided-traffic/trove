package oidcstub

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/asn1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"
)

// DefaultAudience is the "aud" of a token that names none: the base URL of an installation at the
// example host of the spec.
const DefaultAudience = "https://packages.example.com"

// MintOptions says how to build one token. The zero value is a valid GitLab ID token signed
// RS256 with the newest RSA key, "typ": "JWT" and the key's "kid".
//
// Everything after Remove builds the negative cases of R-CI-004.
type MintOptions struct {
	// Claims override the defaults of DefaultClaims by name. A nil value publishes JSON null.
	// "aud" may be a string or a list of strings.
	Claims map[string]any `json:"claims,omitempty"`
	// Remove drops claims, after Claims were applied.
	Remove []string `json:"remove,omitempty"`

	// Alg is the JOSE algorithm: RS256, RS384, RS512, PS256, PS384, PS512, ES256, EdDSA, HS256,
	// HS384, HS512 or none. Empty means the default of the selected key.
	Alg string `json:"alg,omitempty"`
	// KID selects the signing key by ID (a removed key is still found); empty selects the newest
	// key that fits Alg. The header carries the key's own kid.
	KID string `json:"kid,omitempty"`
	// NoKID leaves "kid" out of the header.
	NoKID bool `json:"no_kid,omitempty"`
	// HeaderKID replaces the header's "kid" without changing the signing key, for unknown or
	// over-long values.
	HeaderKID string `json:"header_kid,omitempty"`
	// Typ is the header's "typ", "JWT" when empty; NoTyp leaves it out.
	Typ   string `json:"typ,omitempty"`
	NoTyp bool   `json:"no_typ,omitempty"`
	// Headers are extra header members: jku, x5u, jwk, x5c, crit, ...
	Headers map[string]any `json:"headers,omitempty"`

	// HMACSecret is the secret of an HS* token. Empty means the PEM of the public key of the
	// newest RSA key, the algorithm-confusion attack.
	HMACSecret []byte `json:"hmac_secret,omitempty"`
	// JWE returns a five-part value in the shape of a compact JWE. Nothing is signed.
	JWE bool `json:"jwe,omitempty"`
	// Duplicate appends members to the payload object after the regular ones. A name that is
	// already present is then a duplicate member name.
	Duplicate map[string]any `json:"duplicate,omitempty"`
	// InvalidUTF8 adds a member whose string value holds bytes that are not UTF-8. The signature
	// covers the invalid bytes.
	InvalidUTF8 bool `json:"invalid_utf8,omitempty"`
	// RawPayload replaces the payload bytes entirely.
	RawPayload []byte `json:"raw_payload,omitempty"`
	// PadTo makes the token at least this many bytes long, by a "pad" claim.
	PadTo int `json:"pad_to,omitempty"`
	// ForeignSignature signs with a fresh key of the same type that no JWKS lists, under the
	// selected key's kid: the header names a published key, the signature is not its.
	ForeignSignature bool `json:"foreign_signature,omitempty"`
	// JobToken returns a value in the shape of GitLab 19.0.1's CI_JOB_TOKEN (testdata/job-token.json):
	// JobTokenPrefix followed by a JWT, signed RS256 by a key of the double that its key set does
	// not list (GitLab signs job tokens with ci_job_token_signing_key, which it does not publish).
	// The payload is the ID token's; GitLab's own payload of a job token is not recorded.
	JobToken bool `json:"job_token,omitempty"`

	// SignWith signs with this key instead of selecting one (in-process only).
	SignWith *Key `json:"-"`
}

// DefaultLifetime is exp - iat of a default token: GitLab sets exp to iat plus the job's timeout,
// and a project's default job timeout is 60 minutes (observed on GitLab 19.0.1, R-CI-006).
const DefaultLifetime = time.Hour

// DefaultClaims is the payload GitLab issues to a job with environment production (action start)
// on the protected tag v1.4.0 of the example project, pushed by a maintainer: exactly the claims of
// testdata/id-token-push-protected-tag-environment.json, recorded from GitLab 19.0.1 (R-CI-006;
// fixture_test.go checks it), with the example project's values. GitLab writes IDs and booleans as
// JSON strings; runner_id and the NumericDates are numbers; jti is a UUID; nbf is iat - 5.
//
// GitLab 19.0.1 sends no source_project_id or source_project_path (GitLab documents them from
// 19.5): a test of the cross-project guard adds them through MintOptions.Claims.
func DefaultClaims(issuer string, audience any, now time.Time) map[string]any {
	if audience == nil {
		audience = DefaultAudience
	}
	iat := now.Unix()
	const sha = "0123456789abcdef0123456789abcdef01234567"
	return map[string]any{
		"iss": issuer,
		"aud": audience,
		"sub": "project_path:acme/billing/invoice-api:ref_type:tag:ref:v1.4.0",
		"jti": newUUID(),
		"iat": iat,
		"nbf": iat - 5,
		"exp": iat + int64(DefaultLifetime/time.Second),

		"project_id":     "1234",
		"project_path":   "acme/billing/invoice-api",
		"namespace_id":   "4711",
		"namespace_path": "acme/billing",

		"job_project_id":     "1234",
		"job_project_path":   "acme/billing/invoice-api",
		"job_namespace_id":   "4711",
		"job_namespace_path": "acme/billing",

		"ref":           "v1.4.0",
		"ref_type":      "tag",
		"ref_path":      "refs/tags/v1.4.0",
		"ref_protected": "true",

		"pipeline_source": "push",
		"pipeline_id":     "98765",
		"job_id":          "123456",
		"job_source":      "push",

		"environment":           "production",
		"environment_protected": "true",
		"deployment_tier":       "production",
		"environment_action":    "start",

		"user_id":           "42",
		"user_login":        "jdoe",
		"user_email":        "jdoe@example.com",
		"user_access_level": "maintainer",

		"sha":                sha,
		"ci_config_ref_uri":  hostOf(issuer) + "/acme/billing/invoice-api//.gitlab-ci.yml@refs/tags/v1.4.0",
		"ci_config_sha":      sha,
		"runner_id":          17,
		"runner_environment": "self-hosted",
		"project_visibility": "private",
	}
}

// hostOf is the issuer without its scheme: GitLab writes ci_config_ref_uri as host[:port]/path.
func hostOf(issuer string) string {
	if i := strings.Index(issuer, "://"); i >= 0 {
		return issuer[i+3:]
	}
	return issuer
}

// newUUID is a random (version 4) UUID, the shape of GitLab's jti.
func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// JobTokenPrefix starts every CI_JOB_TOKEN of GitLab 19.0.1, in the JWT format too (R-CI-004 step 2).
const JobTokenPrefix = "glcbt-"

// Mint returns a token as described by o.
func (s *Server) Mint(o MintOptions) (string, error) {
	return s.mint(o, s.Issuer())
}

func (s *Server) mint(o MintOptions, issuer string) (string, error) {
	var aud any
	if s.cfg.Audience != "" {
		aud = s.cfg.Audience
	}
	claims := DefaultClaims(issuer, aud, s.now())
	for k, v := range o.Claims {
		claims[k] = v
	}
	for _, k := range o.Remove {
		delete(claims, k)
	}
	if o.JobToken && o.SignWith == nil {
		k, err := s.jobTokenKey()
		if err != nil {
			return "", err
		}
		o.SignWith = k
	}
	tok, err := s.build(o, claims)
	if err != nil {
		return "", err
	}
	if o.PadTo > len(tok) {
		claims["pad"] = strings.Repeat("a", (o.PadTo-len(tok))*3/4+16)
		if tok, err = s.build(o, claims); err != nil {
			return "", err
		}
	}
	if o.JobToken {
		tok = JobTokenPrefix + tok
	}
	return tok, nil
}

// jobTokenKey is the unpublished key that signs job tokens, made on first use.
func (s *Server) jobTokenKey() (*Key, error) {
	s.mu.Lock()
	k := s.jobKey
	s.mu.Unlock()
	if k != nil {
		return k, nil
	}
	k, err := newKey(KeyOptions{Unpublished: true})
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.jobKey == nil {
		s.jobKey = k
	}
	return s.jobKey, nil
}

func (s *Server) build(o MintOptions, claims map[string]any) (string, error) {
	payload := o.RawPayload
	if payload == nil {
		var err error
		if payload, err = encodePayload(claims, o); err != nil {
			return "", err
		}
	}
	if o.JWE {
		return jwe(payload), nil
	}

	alg := o.Alg
	key := o.SignWith
	if key == nil {
		var err error
		if key, err = s.pickKey(alg, o.KID); err != nil {
			return "", err
		}
	}
	if alg == "" {
		alg = key.Type.DefaultAlg()
	}

	header := map[string]any{"alg": alg}
	if !o.NoTyp {
		header["typ"] = "JWT"
		if o.Typ != "" {
			header["typ"] = o.Typ
		}
	}
	if !o.NoKID && key.KID != "" {
		header["kid"] = key.KID
	}
	if !o.NoKID && o.HeaderKID != "" {
		header["kid"] = o.HeaderKID
	}
	for k, v := range o.Headers {
		header[k] = v
	}
	hj, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	input := b64(hj) + "." + b64(payload)

	switch {
	case alg == "none":
		return input + ".", nil
	case strings.HasPrefix(alg, "HS"):
		secret := o.HMACSecret
		if len(secret) == 0 {
			rk, err := s.pickKey("RS256", "")
			if err != nil {
				return "", err
			}
			secret = rk.PublicKeyPEM()
		}
		return input + "." + b64(hmacSum(alg, secret, input)), nil
	}
	signer := key
	if o.ForeignSignature {
		if signer, err = newKey(KeyOptions{Type: key.Type}); err != nil {
			return "", err
		}
	}
	sig, err := signer.sign(alg, []byte(input))
	if err != nil {
		return "", err
	}
	return input + "." + b64(sig), nil
}

// encodePayload writes the claims as a JSON object. The duplicate and invalid members are
// appended as raw text, so that they survive: a map could not hold a name twice.
func encodePayload(claims map[string]any, o MintOptions) ([]byte, error) {
	body, err := json.Marshal(claims)
	if err != nil {
		return nil, err
	}
	var extra []byte
	names := make([]string, 0, len(o.Duplicate))
	for n := range o.Duplicate {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		nj, _ := json.Marshal(n)
		vj, err := json.Marshal(o.Duplicate[n])
		if err != nil {
			return nil, err
		}
		extra = append(extra, ',')
		extra = append(extra, nj...)
		extra = append(extra, ':')
		extra = append(extra, vj...)
	}
	if o.InvalidUTF8 {
		extra = append(extra, `,"x_invalid":"`...)
		extra = append(extra, 0xc3, 0x28) // a lead byte followed by a non-continuation byte
		extra = append(extra, '"')
	}
	if len(extra) == 0 {
		return body, nil
	}
	if len(claims) == 0 {
		extra = extra[1:]
	}
	return append(body[:len(body)-1], append(extra, '}')...), nil
}

func jwe(payload []byte) string {
	h, _ := json.Marshal(map[string]string{"alg": "RSA-OAEP", "enc": "A256GCM", "typ": "JWT"})
	key := make([]byte, 256)
	iv := make([]byte, 12)
	tag := make([]byte, 16)
	_, _ = rand.Read(key)
	_, _ = rand.Read(iv)
	_, _ = rand.Read(tag)
	return strings.Join([]string{b64(h), b64(key), b64(iv), b64(payload), b64(tag)}, ".")
}

func hmacSum(alg string, secret []byte, input string) []byte {
	h := sha256.New
	switch alg {
	case "HS384":
		h = sha512.New384
	case "HS512":
		h = sha512.New
	}
	m := hmac.New(h, secret)
	m.Write([]byte(input))
	return m.Sum(nil)
}

func (s *Server) pickKey(alg, kid string) (*Key, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if kid != "" {
		for _, k := range s.keys {
			if k.KID == kid {
				return k, nil
			}
		}
		for _, k := range s.retired {
			if k.KID == kid {
				return k, nil
			}
		}
		return nil, fmt.Errorf("oidcstub: no key with kid %q", kid)
	}
	want := RSA2048
	switch alg {
	case "ES256":
		want = ES256
	case "EdDSA":
		want = EdDSA
	}
	for i := len(s.keys) - 1; i >= 0; i-- {
		k := s.keys[i]
		// The newest published key of the family; the 1024-bit, the 4096-bit and the unpublished keys
		// are only chosen by kid.
		if k.Type == want && k.Published() {
			return k, nil
		}
	}
	return nil, fmt.Errorf("oidcstub: no %s key for algorithm %q; add one first", want, alg)
}

type ecSig struct{ R, S *big.Int }

func (k *Key) sign(alg string, input []byte) ([]byte, error) {
	switch alg {
	case "RS256", "RS384", "RS512", "PS256", "PS384", "PS512":
		priv, ok := k.signer.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("oidcstub: %s needs an RSA key, have %s", alg, k.Type)
		}
		hash, digest := digestOf(alg, input)
		if strings.HasPrefix(alg, "PS") {
			return rsa.SignPSS(rand.Reader, priv, hash, digest, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
		}
		return rsa.SignPKCS1v15(rand.Reader, priv, hash, digest)
	case "ES256":
		priv, ok := k.signer.(*ecdsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("oidcstub: ES256 needs an EC key, have %s", k.Type)
		}
		sum := sha256.Sum256(input)
		der, err := ecdsa.SignASN1(rand.Reader, priv, sum[:])
		if err != nil {
			return nil, err
		}
		var sig ecSig
		if _, err := asn1.Unmarshal(der, &sig); err != nil {
			return nil, err
		}
		out := make([]byte, 64)
		sig.R.FillBytes(out[:32])
		sig.S.FillBytes(out[32:])
		return out, nil
	case "EdDSA":
		priv, ok := k.signer.(ed25519.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("oidcstub: EdDSA needs an Ed25519 key, have %s", k.Type)
		}
		return ed25519.Sign(priv, input), nil
	}
	return nil, errors.New("oidcstub: cannot sign with algorithm " + alg)
}

func digestOf(alg string, input []byte) (crypto.Hash, []byte) {
	switch alg[2:] {
	case "384":
		sum := sha512.Sum384(input)
		return crypto.SHA384, sum[:]
	case "512":
		sum := sha512.Sum512(input)
		return crypto.SHA512, sum[:]
	}
	sum := sha256.Sum256(input)
	return crypto.SHA256, sum[:]
}
