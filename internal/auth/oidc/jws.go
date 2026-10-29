package oidc

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strings"

	// Hash implementations the signature algorithms below name.
	_ "crypto/sha256"
	_ "crypto/sha512"
)

// The minimal verifier of signed ID tokens (R-AUTH-006, Q-AUTH-31): the JWS
// compact serialization (RFC 7515 §7.1) with the RSA PKCS#1 v1.5, RSA-PSS and
// ECDSA algorithms of RFC 7518 §3, keys from a JWK Set (RFC 7517). It refuses
// everything else by construction: the JWE form (five parts), "none", every
// HMAC algorithm, a "crit" header, a key whose type or curve does not fit the
// algorithm, and a key whose "use" is not "sig". Only the standard library's
// cryptography runs.

// errJWS is the reason code of every form, algorithm and signature failure
// (id_token_invalid); the cause stays in the log.
var errJWS = errors.New("id_token_invalid")

// jwsHeader is the protected header of a compact JWS.
type jwsHeader struct {
	Alg  string          `json:"alg"`
	Kid  string          `json:"kid"`
	Typ  string          `json:"typ"`
	Crit json.RawMessage `json:"crit"`
}

// parsedJWS is a compact JWS split and decoded, not yet verified.
type parsedJWS struct {
	header       jwsHeader
	payload      []byte
	signingInput string
	signature    []byte
}

// parseJWS splits and decodes a compact JWS. A token with five parts is a JWE,
// which is not supported (R-AUTH-006).
func parseJWS(token string) (parsedJWS, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return parsedJWS{}, fmt.Errorf("%w: %d parts (an encrypted ID token or no JWS)", errJWS, len(parts))
	}
	hb, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return parsedJWS{}, fmt.Errorf("%w: header: %w", errJWS, err)
	}
	var h jwsHeader
	if err := json.Unmarshal(hb, &h); err != nil {
		return parsedJWS{}, fmt.Errorf("%w: header: %w", errJWS, err)
	}
	if len(h.Crit) > 0 {
		return parsedJWS{}, fmt.Errorf("%w: crit header parameters are not supported", errJWS)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return parsedJWS{}, fmt.Errorf("%w: payload: %w", errJWS, err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return parsedJWS{}, fmt.Errorf("%w: signature: %w", errJWS, err)
	}
	return parsedJWS{header: h, payload: payload, signingInput: parts[0] + "." + parts[1], signature: sig}, nil
}

// algSpec is how one algorithm verifies.
type algSpec struct {
	hash  crypto.Hash
	kty   string // RSA or EC
	pss   bool
	curve elliptic.Curve
}

// algorithm returns the verification of a supported algorithm. none and HS*
// are never supported, whatever allowed_signing_algs says.
func algorithm(alg string) (algSpec, bool) {
	switch alg {
	case "RS256":
		return algSpec{hash: crypto.SHA256, kty: "RSA"}, true
	case "RS384":
		return algSpec{hash: crypto.SHA384, kty: "RSA"}, true
	case "RS512":
		return algSpec{hash: crypto.SHA512, kty: "RSA"}, true
	case "PS256":
		return algSpec{hash: crypto.SHA256, kty: "RSA", pss: true}, true
	case "PS384":
		return algSpec{hash: crypto.SHA384, kty: "RSA", pss: true}, true
	case "PS512":
		return algSpec{hash: crypto.SHA512, kty: "RSA", pss: true}, true
	case "ES256":
		return algSpec{hash: crypto.SHA256, kty: "EC", curve: elliptic.P256()}, true
	case "ES384":
		return algSpec{hash: crypto.SHA384, kty: "EC", curve: elliptic.P384()}, true
	}
	return algSpec{}, false
}

// verify checks the signature of j with key under the header's algorithm, which
// must be in allowed.
func verify(j parsedJWS, key *jwk, allowed []string) error {
	alg := j.header.Alg
	spec, ok := algorithm(alg)
	if !ok || !slices.Contains(allowed, alg) {
		return fmt.Errorf("%w: alg %q is not allowed", errJWS, alg)
	}
	if key.alg != "" && key.alg != alg {
		return fmt.Errorf("%w: the key %q is for %s, the token uses %s", errJWS, key.kid, key.alg, alg)
	}
	h := spec.hash.New()
	h.Write([]byte(j.signingInput))
	digest := h.Sum(nil)
	switch spec.kty {
	case "RSA":
		if key.rsa == nil {
			return fmt.Errorf("%w: %s needs an RSA key", errJWS, alg)
		}
		var err error
		if spec.pss {
			err = rsa.VerifyPSS(key.rsa, spec.hash, digest, j.signature, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
		} else {
			err = rsa.VerifyPKCS1v15(key.rsa, spec.hash, digest, j.signature)
		}
		if err != nil {
			return fmt.Errorf("%w: signature: %w", errJWS, err)
		}
	case "EC":
		if key.ec == nil || key.ec.Curve != spec.curve {
			return fmt.Errorf("%w: %s needs an EC key on %s", errJWS, alg, spec.curve.Params().Name)
		}
		// RFC 7518 §3.4: R and S, each the size of the curve order, concatenated.
		n := (spec.curve.Params().BitSize + 7) / 8
		if len(j.signature) != 2*n {
			return fmt.Errorf("%w: ECDSA signature of %d bytes, want %d", errJWS, len(j.signature), 2*n)
		}
		r, s := new(big.Int).SetBytes(j.signature[:n]), new(big.Int).SetBytes(j.signature[n:])
		if !ecdsa.Verify(key.ec, digest, r, s) {
			return fmt.Errorf("%w: signature does not verify", errJWS)
		}
	}
	return nil
}

// jwk is one verification key of a JWK Set.
type jwk struct {
	kid string
	alg string
	rsa *rsa.PublicKey
	ec  *ecdsa.PublicKey
}

// rawJWK is the wire form of a JWK (RFC 7517 §4, RFC 7518 §6).
type rawJWK struct {
	Kty string `json:"kty"`
	Use string `json:"use"`
	Kid string `json:"kid"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

// minRSABits refuses RSA keys below 2048 bits (RFC 7518 §3.3).
const minRSABits = 2048

// parseJWKS decodes a JWK Set into its signature keys. A key of another type or
// use, or one that does not parse, is left out; the others still count.
func parseJWKS(body []byte) ([]*jwk, error) {
	var set struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if err := json.Unmarshal(body, &set); err != nil {
		return nil, fmt.Errorf("JWK Set: %w", err)
	}
	var out []*jwk
	for _, raw := range set.Keys {
		var k rawJWK
		if json.Unmarshal(raw, &k) != nil || (k.Use != "" && k.Use != "sig") {
			continue
		}
		key := &jwk{kid: k.Kid, alg: k.Alg}
		switch k.Kty {
		case "RSA":
			pub, err := rsaKey(k)
			if err != nil {
				continue
			}
			key.rsa = pub
		case "EC":
			pub, err := ecKey(k)
			if err != nil {
				continue
			}
			key.ec = pub
		default:
			continue
		}
		out = append(out, key)
	}
	return out, nil
}

func rsaKey(k rawJWK) (*rsa.PublicKey, error) {
	n, err := base64.RawURLEncoding.DecodeString(k.N)
	if err != nil {
		return nil, err
	}
	e, err := base64.RawURLEncoding.DecodeString(k.E)
	if err != nil || len(e) == 0 || len(e) > 4 {
		return nil, errors.New("bad exponent")
	}
	exp := 0
	for _, b := range e {
		exp = exp<<8 | int(b)
	}
	pub := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: exp}
	if pub.N.BitLen() < minRSABits || exp < 3 {
		return nil, errors.New("RSA key too weak")
	}
	return pub, nil
}

func ecKey(k rawJWK) (*ecdsa.PublicKey, error) {
	var curve elliptic.Curve
	switch k.Crv {
	case "P-256":
		curve = elliptic.P256()
	case "P-384":
		curve = elliptic.P384()
	default:
		return nil, errors.New("unsupported curve")
	}
	x, err := base64.RawURLEncoding.DecodeString(k.X)
	if err != nil {
		return nil, err
	}
	y, err := base64.RawURLEncoding.DecodeString(k.Y)
	if err != nil {
		return nil, err
	}
	size := (curve.Params().BitSize + 7) / 8
	if len(x) != size || len(y) != size {
		return nil, errors.New("bad coordinate length")
	}
	// The uncompressed SEC 1 form lets the standard library check that the point
	// is on the curve.
	point := append([]byte{4}, append(x, y...)...)
	pub, err := ecdsa.ParseUncompressedPublicKey(curve, point)
	if err != nil {
		return nil, err
	}
	return pub, nil
}

// selectKey returns the key of a token's kid. A token without kid is matched
// only when the set holds exactly one key that fits its algorithm.
func selectKey(keys []*jwk, kid, alg string) *jwk {
	spec, ok := algorithm(alg)
	if !ok {
		return nil
	}
	var fit []*jwk
	for _, k := range keys {
		if (spec.kty == "RSA") != (k.rsa != nil) {
			continue
		}
		if kid != "" {
			if k.kid == kid {
				return k
			}
			continue
		}
		fit = append(fit, k)
	}
	if kid == "" && len(fit) == 1 {
		return fit[0]
	}
	return nil
}
