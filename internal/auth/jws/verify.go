package jws

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"math/big"

	// The hash implementations the algorithms below name.
	_ "crypto/sha256"
	_ "crypto/sha512"
)

// Key types of a JWK (RFC 7518 §6.1, RFC 8037 §2).
const (
	KTYRSA = "RSA"
	KTYEC  = "EC"
	KTYOKP = "OKP"
)

// MinRSABits is the smallest RSA modulus a key may have (RFC 7518 §3.3, R-CI-003).
const MinRSABits = 2048

// algorithm is how one algorithm verifies.
type algorithm struct {
	kty   string
	hash  crypto.Hash // 0 for EdDSA, which hashes internally
	pss   bool
	curve elliptic.Curve
}

// lookup returns the verification of a supported algorithm. none and the HMAC algorithms are
// never supported: a caller's allow list cannot add them.
func lookup(alg string) (algorithm, bool) {
	switch alg {
	case "RS256":
		return algorithm{kty: KTYRSA, hash: crypto.SHA256}, true
	case "RS384":
		return algorithm{kty: KTYRSA, hash: crypto.SHA384}, true
	case "RS512":
		return algorithm{kty: KTYRSA, hash: crypto.SHA512}, true
	case "PS256":
		return algorithm{kty: KTYRSA, hash: crypto.SHA256, pss: true}, true
	case "PS384":
		return algorithm{kty: KTYRSA, hash: crypto.SHA384, pss: true}, true
	case "PS512":
		return algorithm{kty: KTYRSA, hash: crypto.SHA512, pss: true}, true
	case "ES256":
		return algorithm{kty: KTYEC, hash: crypto.SHA256, curve: elliptic.P256()}, true
	case "ES384":
		return algorithm{kty: KTYEC, hash: crypto.SHA384, curve: elliptic.P384()}, true
	case "EdDSA":
		return algorithm{kty: KTYOKP}, true
	}
	return algorithm{}, false
}

// Algorithms returns the supported algorithms (R-CI-001's list) in a stable order.
func Algorithms() []string {
	return []string{"RS256", "RS384", "RS512", "PS256", "PS384", "PS512", "ES256", "ES384", "EdDSA"}
}

// Supported reports whether the algorithm is one this package verifies.
func Supported(alg string) bool {
	_, ok := lookup(alg)
	return ok
}

// KeyType returns the JWK key type an algorithm needs (RSA, EC or OKP).
func KeyType(alg string) (string, bool) {
	a, ok := lookup(alg)
	return a.kty, ok
}

// Fits reports whether a public key can verify tokens of the algorithm: the algorithm is
// supported, the key's type and curve are the algorithm's, an RSA key has at least MinRSABits, and
// the key's own alg (keyAlg, "" when the JWK has none) equals the algorithm.
func Fits(alg string, pub crypto.PublicKey, keyAlg string) bool {
	a, ok := lookup(alg)
	return ok && (keyAlg == "" || keyAlg == alg) && a.fits(pub)
}

func (a algorithm) fits(pub crypto.PublicKey) bool {
	switch k := pub.(type) {
	case *rsa.PublicKey:
		return a.kty == KTYRSA && k != nil && k.N != nil && k.N.BitLen() >= MinRSABits
	case *ecdsa.PublicKey:
		return a.kty == KTYEC && k != nil && k.Curve == a.curve
	case ed25519.PublicKey:
		return a.kty == KTYOKP && len(k) == ed25519.PublicKeySize
	}
	return false
}

// VerifyWith checks the token's signature with a public key under the header's algorithm. keyAlg
// is the key's own alg member ("" when absent). It refuses an unsupported algorithm (ErrAlgorithm),
// a key that does not fit it (ErrKey) and a signature that does not verify (ErrSignature). Whether
// the algorithm is one the caller allows is the caller's check, made before.
func (t *Token) VerifyWith(pub crypto.PublicKey, keyAlg string) error {
	alg := t.Header.Alg
	a, ok := lookup(alg)
	if !ok {
		return errorOf(ErrAlgorithm, "the algorithm is not one of RS256/384/512, PS256/384/512, ES256/384, EdDSA (none and HMAC never are)")
	}
	if keyAlg != "" && keyAlg != alg {
		return errorOf(ErrKey, "the key's own alg is not the token's")
	}
	if !a.fits(pub) {
		return errorOf(ErrKey, "the key's type, curve or size does not fit the algorithm")
	}
	if len(t.signature) == 0 {
		return errorOf(ErrSignature, "the token has no signature")
	}
	if a.kty == KTYOKP {
		if !ed25519.Verify(pub.(ed25519.PublicKey), []byte(t.signingInput), t.signature) {
			return errorOf(ErrSignature, "the EdDSA signature does not verify")
		}
		return nil
	}
	h := a.hash.New()
	h.Write([]byte(t.signingInput))
	digest := h.Sum(nil)
	switch a.kty {
	case KTYRSA:
		var err error
		if a.pss {
			err = rsa.VerifyPSS(pub.(*rsa.PublicKey), a.hash, digest, t.signature, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
		} else {
			err = rsa.VerifyPKCS1v15(pub.(*rsa.PublicKey), a.hash, digest, t.signature)
		}
		if err != nil {
			return errorOf(ErrSignature, "the RSA signature does not verify")
		}
	case KTYEC:
		// RFC 7518 §3.4: R and S, each the size of the curve order, concatenated.
		n := (a.curve.Params().BitSize + 7) / 8
		if len(t.signature) != 2*n {
			return errorOf(ErrSignature, "the ECDSA signature has the wrong length")
		}
		r, s := new(big.Int).SetBytes(t.signature[:n]), new(big.Int).SetBytes(t.signature[n:])
		if !ecdsa.Verify(pub.(*ecdsa.PublicKey), digest, r, s) {
			return errorOf(ErrSignature, "the ECDSA signature does not verify")
		}
	}
	return nil
}
