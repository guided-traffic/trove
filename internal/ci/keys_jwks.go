package ci

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"slices"

	"github.com/guided-traffic/trove/internal/ci/subject"
)

// The key rules of R-CI-003 and the stored form of a key set (R-CI-003 "Data model": the last good key
// set, public keys only). A fetched JWK Set is reduced to the public members this package reads before it
// is stored, so a private member that a hostile or careless issuer publishes never reaches the table, and
// two fetches of the same keys compare equal whatever the issuer's member order or extra members are.

// maxStoredKeys bounds the keys kept of one fetched set. The response is bounded in bytes already
// (ci.signing_keys.max_response_bytes); this bounds the stored row and the work per token.
const maxStoredKeys = 100

// minRSABits is the smallest RSA modulus a key may have (R-CI-003, RFC 7518 §3.3).
const minRSABits = 2048

// jwkDoc is the stored form of one key: the public members that the key rules and the verification read.
// KeyOps is a pointer so that an absent member and an empty list differ: an empty `key_ops` does not
// contain `verify`.
type jwkDoc struct {
	KTY    string    `json:"kty"`
	KID    string    `json:"kid,omitempty"`
	Use    string    `json:"use,omitempty"`
	KeyOps *[]string `json:"key_ops,omitempty"`
	Alg    string    `json:"alg,omitempty"`
	N      string    `json:"n,omitempty"`
	E      string    `json:"e,omitempty"`
	Crv    string    `json:"crv,omitempty"`
	X      string    `json:"x,omitempty"`
	Y      string    `json:"y,omitempty"`
}

// storedSet is the stored JSON of a key set.
type storedSet struct {
	Keys []jwkDoc `json:"keys"`
}

var errNotAKeySet = errors.New("the document is not a JWK Set")

// reduceJWKS decodes a fetched JWK Set document (RFC 7517 §5) and returns the stored form: the
// whitelisted public members of each key, at most maxStoredKeys keys. An element that is not an object
// or has no string `kty` is dropped; a member of the wrong type is dropped from its key. The error never
// carries bytes of the document.
func reduceJWKS(body []byte) ([]byte, error) {
	var top struct {
		Keys *[]json.RawMessage `json:"keys"`
	}
	if err := json.Unmarshal(body, &top); err != nil || top.Keys == nil {
		return nil, errNotAKeySet
	}
	out := storedSet{Keys: make([]jwkDoc, 0, len(*top.Keys))}
	for _, raw := range *top.Keys {
		if len(out.Keys) >= maxStoredKeys {
			break
		}
		var m map[string]json.RawMessage
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		str := func(name string) string {
			var v string
			if json.Unmarshal(m[name], &v) != nil {
				return ""
			}
			return v
		}
		k := jwkDoc{
			KTY: str("kty"), KID: str("kid"), Use: str("use"), Alg: str("alg"),
			N: str("n"), E: str("e"), Crv: str("crv"), X: str("x"), Y: str("y"),
		}
		if k.KTY == "" {
			continue
		}
		if ops, ok := m["key_ops"]; ok {
			var list []string
			if json.Unmarshal(ops, &list) == nil {
				if list == nil {
					list = []string{}
				}
				k.KeyOps = &list
			} else {
				// A key_ops member of the wrong type is not a list that contains verify.
				k.KeyOps = &[]string{}
			}
		}
		out.Keys = append(out.Keys, k)
	}
	return json.Marshal(out)
}

// keySet is a stored key set parsed under the key rules: the usable keys for the verification and the
// status of every key.
type keySet struct {
	Infos  []KeyInfo
	Usable []subject.Key
}

// parseStored applies the key rules of R-CI-003 to a stored set: `use` absent or `sig`, `key_ops`
// absent or containing `verify`, a key type with a verification algorithm of R-CI-001 (RSA, EC with P-256
// or P-384, OKP with Ed25519), RSA with at least 2048 bits, and one key per `kid`. The rules that depend
// on the token (the key's own `alg` equal to the token's, the key type fitting the algorithm) are the
// validation's (subject.Unverified.Verify). A key that fails a rule is kept in Infos with its reason and
// is not in Usable.
func parseStored(stored []byte) (keySet, error) {
	var set storedSet
	if err := json.Unmarshal(stored, &set); err != nil {
		return keySet{}, fmt.Errorf("stored key set: %w", err)
	}
	out := keySet{Infos: make([]KeyInfo, 0, len(set.Keys))}
	seen := map[string]bool{}
	for _, k := range set.Keys {
		info := KeyInfo{KID: k.KID, KTY: k.KTY, Alg: k.Alg}
		pub, reason := usable(k)
		if reason == "" && k.KID != "" && seen[k.KID] {
			reason = "a key with this kid is listed before"
		}
		if reason != "" {
			info.Ignored, info.IgnoredReason = true, reason
			out.Infos = append(out.Infos, info)
			continue
		}
		seen[k.KID] = true
		out.Infos = append(out.Infos, info)
		out.Usable = append(out.Usable, subject.Key{KID: k.KID, KTY: k.KTY, Alg: k.Alg, Public: pub})
	}
	return out, nil
}

// usable checks one key; reason is empty when the key is usable.
func usable(k jwkDoc) (crypto.PublicKey, string) {
	if k.Use != "" && k.Use != "sig" {
		return nil, `use is not "sig"`
	}
	if k.KeyOps != nil && !slices.Contains(*k.KeyOps, "verify") {
		return nil, `key_ops does not contain "verify"`
	}
	switch k.KTY {
	case "RSA":
		pub, err := rsaKey(k)
		if err != nil {
			return nil, err.Error()
		}
		return pub, ""
	case "EC":
		pub, err := ecKey(k)
		if err != nil {
			return nil, err.Error()
		}
		return pub, ""
	case "OKP":
		pub, err := okpKey(k)
		if err != nil {
			return nil, err.Error()
		}
		return pub, ""
	}
	return nil, "the key type has no verification algorithm"
}

func rsaKey(k jwkDoc) (*rsa.PublicKey, error) {
	n, err := base64.RawURLEncoding.DecodeString(k.N)
	if err != nil || len(n) == 0 {
		return nil, errors.New("the RSA modulus is not base64url")
	}
	e, err := base64.RawURLEncoding.DecodeString(k.E)
	if err != nil || len(e) == 0 || len(e) > 4 {
		return nil, errors.New("the RSA exponent is not usable")
	}
	exp := 0
	for _, b := range e {
		exp = exp<<8 | int(b)
	}
	pub := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: exp}
	if pub.N.BitLen() < minRSABits {
		return nil, fmt.Errorf("the RSA key has fewer than %d bits", minRSABits)
	}
	if exp < 3 || exp%2 == 0 {
		return nil, errors.New("the RSA exponent is not usable")
	}
	return pub, nil
}

func ecKey(k jwkDoc) (*ecdsa.PublicKey, error) {
	var curve elliptic.Curve
	switch k.Crv {
	case "P-256":
		curve = elliptic.P256()
	case "P-384":
		curve = elliptic.P384()
	default:
		return nil, errors.New("the curve has no verification algorithm (ES256 and ES384 are accepted)")
	}
	x, errX := base64.RawURLEncoding.DecodeString(k.X)
	y, errY := base64.RawURLEncoding.DecodeString(k.Y)
	size := (curve.Params().BitSize + 7) / 8
	if errX != nil || errY != nil || len(x) != size || len(y) != size {
		return nil, errors.New("the EC coordinates are not usable")
	}
	// The uncompressed SEC 1 form lets the standard library check that the point is on the curve.
	point := append([]byte{4}, append(x, y...)...)
	pub, err := ecdsa.ParseUncompressedPublicKey(curve, point)
	if err != nil {
		return nil, errors.New("the EC point is not on the curve")
	}
	return pub, nil
}

func okpKey(k jwkDoc) (ed25519.PublicKey, error) {
	if k.Crv != "Ed25519" {
		return nil, errors.New("the curve has no verification algorithm (EdDSA is accepted with Ed25519)")
	}
	x, err := base64.RawURLEncoding.DecodeString(k.X)
	if err != nil || len(x) != ed25519.PublicKeySize {
		return nil, errors.New("the Ed25519 key is not usable")
	}
	return ed25519.PublicKey(x), nil
}

// kids lists the key IDs of the usable keys, for the audit event of a fetch (at most the stored keys).
func (s keySet) kids() []string {
	out := make([]string, 0, len(s.Usable))
	for _, k := range s.Usable {
		out = append(out, k.KID)
	}
	return out
}

// find returns the keys that may verify a token: the usable key of that kid, or, without a kid, every
// usable key whose key type fits alg, at most subject.MaxKeysWithoutKID.
func (s keySet) find(kid, alg string) []subject.Key {
	if kid != "" {
		for _, k := range s.Usable {
			if k.KID == kid {
				return []subject.Key{k}
			}
		}
		return nil
	}
	var out []subject.Key
	for _, k := range s.Usable {
		if familyFits(k.KTY, alg) {
			out = append(out, k)
			if len(out) == subject.MaxKeysWithoutKID {
				break
			}
		}
	}
	return out
}

// familyFits reports whether a key type can verify a token of the algorithm: RSA the RS and PS
// algorithms, EC the ES algorithms, OKP EdDSA. An unknown algorithm fits nothing.
func familyFits(kty, alg string) bool {
	switch kty {
	case "RSA":
		return len(alg) == 5 && (alg[:2] == "RS" || alg[:2] == "PS")
	case "EC":
		return alg == "ES256" || alg == "ES384"
	case "OKP":
		return alg == "EdDSA"
	}
	return false
}
