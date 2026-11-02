package jws

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math"
	"math/big"
	"slices"
)

// JWK is one usable verification key of a JWK set.
type JWK struct {
	// KID is the key ID, "" for a key published without one.
	KID string
	// KTY is RSA, EC or OKP.
	KTY string
	// Alg is the key's own alg member, "" when absent.
	Alg string
	// Crv is the curve of an EC or OKP key (P-256, P-384, Ed25519).
	Crv string
	// Public is *rsa.PublicKey, *ecdsa.PublicKey or ed25519.PublicKey.
	Public crypto.PublicKey
}

// The reasons a key of a JWK set is ignored (R-CI-003). They are stable names: the key status of a
// CI issuer shows them (KeyInfo.IgnoredReason).
const (
	// IgnoredUse: `use` is present and not `sig`.
	IgnoredUse = "use_not_sig"
	// IgnoredKeyOps: `key_ops` is present and does not contain `verify`.
	IgnoredKeyOps = "key_ops_without_verify"
	// IgnoredKeyType: `kty` is not RSA, EC or OKP (an `oct` key among them).
	IgnoredKeyType = "unsupported_key_type"
	// IgnoredCurve: an EC curve other than P-256 and P-384, or an OKP curve other than Ed25519.
	IgnoredCurve = "unsupported_curve"
	// IgnoredRSASize: an RSA modulus below MinRSABits.
	IgnoredRSASize = "rsa_key_too_small"
	// IgnoredAlgorithm: the key's own `alg` is not a supported signature algorithm.
	IgnoredAlgorithm = "unsupported_algorithm"
	// IgnoredAlgorithmMismatch: the key's own `alg` names another key type or curve than the key has.
	IgnoredAlgorithmMismatch = "algorithm_key_mismatch"
	// IgnoredPrivate: the entry carries private key material (`d`, `p`, `q`, `dp`, `dq`, `qi`, `oth`,
	// `k`); a key whose private half was published is compromised and verifies nothing.
	IgnoredPrivate = "private_key_material"
	// IgnoredInvalid: the entry is not a JSON object, a member has the wrong type, or the key
	// material does not decode (a bad exponent, a coordinate of the wrong length, a point not on
	// the curve).
	IgnoredInvalid = "invalid_key_material"
)

// IgnoredKey is a key of a JWK set that does not meet the key rules, with what could be read of it.
type IgnoredKey struct {
	KID    string
	KTY    string
	Alg    string
	Reason string
}

// Set is a parsed JWK set: the usable keys in the order of the set, and the ignored ones.
type Set struct {
	Keys    []*JWK
	Ignored []IgnoredKey
}

// rawJWK is the wire form of a JWK (RFC 7517 §4, RFC 7518 §6, RFC 8037 §2).
type rawJWK struct {
	Kty    string    `json:"kty"`
	Use    *string   `json:"use"`
	KeyOps *[]string `json:"key_ops"`
	Kid    string    `json:"kid"`
	Alg    string    `json:"alg"`
	N      string    `json:"n"`
	E      string    `json:"e"`
	Crv    string    `json:"crv"`
	X      string    `json:"x"`
	Y      string    `json:"y"`
}

// privateMembers are the members of a private or symmetric JWK (RFC 7518 §6.2.2, §6.3.2, §6.4).
var privateMembers = [...]string{"d", "p", "q", "dp", "dq", "qi", "oth", "k"}

// ParseJWKS decodes a JWK set into its usable verification keys and reports the others. A body
// that is not a JSON object is an error; a missing or empty `keys` list is a set without keys. A
// key whose entry does not meet the rules is left out and listed in Ignored; the others still
// count, so one odd key does not stop a rotation.
func ParseJWKS(body []byte) (Set, error) {
	var doc struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return Set{}, errorOf(ErrMalformed, "the JWK set is not a JSON object with a keys list")
	}
	var set Set
	for _, raw := range doc.Keys {
		key, ignored := parseJWK(raw)
		if key != nil {
			set.Keys = append(set.Keys, key)
			continue
		}
		set.Ignored = append(set.Ignored, ignored)
	}
	return set, nil
}

// parseJWK applies the key rules to one entry, in a fixed order, and returns the key or why it is
// ignored.
func parseJWK(raw json.RawMessage) (*JWK, IgnoredKey) {
	var members map[string]json.RawMessage
	if json.Unmarshal(raw, &members) != nil || members == nil {
		return nil, IgnoredKey{Reason: IgnoredInvalid}
	}
	var k rawJWK
	if json.Unmarshal(raw, &k) != nil {
		// What can be read for the report: the string members that have the right type.
		var id struct{ Kid, Kty, Alg any }
		_ = json.Unmarshal(raw, &id)
		kid, _ := id.Kid.(string)
		kty, _ := id.Kty.(string)
		alg, _ := id.Alg.(string)
		return nil, IgnoredKey{KID: kid, KTY: kty, Alg: alg, Reason: IgnoredInvalid}
	}
	ign := func(reason string) (*JWK, IgnoredKey) {
		return nil, IgnoredKey{KID: k.Kid, KTY: k.Kty, Alg: k.Alg, Reason: reason}
	}
	for _, m := range privateMembers {
		if _, has := members[m]; has {
			return ign(IgnoredPrivate)
		}
	}
	if k.Use != nil && *k.Use != "sig" {
		return ign(IgnoredUse)
	}
	if k.KeyOps != nil && !slices.Contains(*k.KeyOps, "verify") {
		return ign(IgnoredKeyOps)
	}
	if k.Kty != KTYRSA && k.Kty != KTYEC && k.Kty != KTYOKP {
		return ign(IgnoredKeyType)
	}
	if reason := algorithmReason(k); reason != "" {
		return ign(reason)
	}
	pub, reason := publicKey(k)
	if reason != "" {
		return ign(reason)
	}
	return &JWK{KID: k.Kid, KTY: k.Kty, Alg: k.Alg, Crv: k.Crv, Public: pub}, IgnoredKey{}
}

// algorithmReason checks the key's own alg against its type and curve.
func algorithmReason(k rawJWK) string {
	if k.Alg == "" {
		return ""
	}
	a, ok := lookup(k.Alg)
	if !ok {
		return IgnoredAlgorithm
	}
	if a.kty != k.Kty {
		return IgnoredAlgorithmMismatch
	}
	if a.kty == KTYEC && curveName(a.curve) != k.Crv {
		return IgnoredAlgorithmMismatch
	}
	return ""
}

func curveName(c elliptic.Curve) string {
	switch c {
	case elliptic.P256():
		return "P-256"
	case elliptic.P384():
		return "P-384"
	}
	return ""
}

// publicKey decodes the key material of a JWK of a supported type.
func publicKey(k rawJWK) (crypto.PublicKey, string) {
	switch k.Kty {
	case KTYRSA:
		return rsaKey(k)
	case KTYEC:
		return ecKey(k)
	}
	return okpKey(k)
}

// b64 decodes a JWK member. It is base64url without padding (RFC 7518 §2), decoded as the login
// always did; the key rules, not the encoding, decide what a key may do.
func b64(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }

func rsaKey(k rawJWK) (crypto.PublicKey, string) {
	n, err := b64(k.N)
	if err != nil || len(n) == 0 {
		return nil, IgnoredInvalid
	}
	e, err := b64(k.E)
	if err != nil || len(e) == 0 || len(e) > 4 {
		return nil, IgnoredInvalid
	}
	exp := 0
	for _, b := range e {
		exp = exp<<8 | int(b)
	}
	if exp < 3 || exp > math.MaxInt32 {
		return nil, IgnoredInvalid
	}
	pub := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: exp}
	if pub.N.BitLen() < MinRSABits {
		return nil, IgnoredRSASize
	}
	return pub, ""
}

func ecKey(k rawJWK) (crypto.PublicKey, string) {
	var curve elliptic.Curve
	switch k.Crv {
	case "P-256":
		curve = elliptic.P256()
	case "P-384":
		curve = elliptic.P384()
	default:
		return nil, IgnoredCurve
	}
	x, errX := b64(k.X)
	y, errY := b64(k.Y)
	size := (curve.Params().BitSize + 7) / 8
	if errX != nil || errY != nil || len(x) != size || len(y) != size {
		return nil, IgnoredInvalid
	}
	// The uncompressed SEC 1 form lets the standard library check that the point is on the curve.
	point := append(append([]byte{4}, x...), y...)
	pub, err := ecdsa.ParseUncompressedPublicKey(curve, point)
	if err != nil {
		return nil, IgnoredInvalid
	}
	return pub, ""
}

func okpKey(k rawJWK) (crypto.PublicKey, string) {
	if k.Crv != "Ed25519" {
		return nil, IgnoredCurve
	}
	x, err := b64(k.X)
	if err != nil || len(x) != ed25519.PublicKeySize {
		return nil, IgnoredInvalid
	}
	return ed25519.PublicKey(x), ""
}

// SelectKey is the login's key selection (R-AUTH-006): the key of the token's kid whose type fits
// the algorithm, or, for a token without kid, the one key of the set whose type fits; nil when
// there is none or, without kid, more than one.
func SelectKey(keys []*JWK, kid, alg string) *JWK {
	kty, ok := KeyType(alg)
	if !ok {
		return nil
	}
	var fit []*JWK
	for _, k := range keys {
		if k.KTY != kty {
			continue
		}
		if kid != "" {
			if k.KID == kid {
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

// Candidates is the key selection of the CI exchange (R-CI-004 step 6): with a kid, the keys of
// that kid that fit the algorithm; without one, every key that fits it. At most max keys are
// returned, in the order of the set.
func Candidates(keys []*JWK, kid, alg string, maxKeys int) []*JWK {
	var out []*JWK
	for _, k := range keys {
		if len(out) == maxKeys {
			break
		}
		if kid != "" && k.KID != kid {
			continue
		}
		if Fits(alg, k.Public, k.Alg) {
			out = append(out, k)
		}
	}
	return out
}
