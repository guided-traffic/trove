package oidcstub

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
)

// KeyType is the kind of a signing key.
type KeyType string

// The key types the double can publish. RSA1024 exists only for the "at least 2048 bits" rule of
// R-CI-003. RSA4096 is the size of a fresh GitLab installation's OpenID Connect key
// (testdata/issuer.json); it takes a second or more to generate, so no default uses it.
const (
	RSA2048 KeyType = "rsa-2048"
	RSA4096 KeyType = "rsa-4096"
	RSA1024 KeyType = "rsa-1024"
	ES256   KeyType = "es256"
	EdDSA   KeyType = "eddsa"
)

// KeyOptions describes a key to add. The zero value is an RSA-2048 key published as
// {"kty", "kid": <RFC 7638 thumbprint>, "e", "n", "use": "sig", "alg": "RS256"}: the members, the kid
// and the size of the CI signing key of GitLab 19.0.1 (testdata/issuer.json, fixture_test.go).
type KeyOptions struct {
	// Type defaults to RSA2048.
	Type KeyType `json:"type,omitempty"`
	// KID is the key ID; empty means the key's RFC 7638 thumbprint.
	KID string `json:"kid,omitempty"`
	// NoKID publishes the key without a "kid" member.
	NoKID bool `json:"no_kid,omitempty"`
	// Use is the "use" member, "sig" when empty; OmitUse leaves it out. "enc" makes a key that
	// must be ignored.
	Use     string `json:"use,omitempty"`
	OmitUse bool   `json:"omit_use,omitempty"`
	// KeyOps is the "key_ops" member; nil leaves it out, an empty list publishes [].
	KeyOps []string `json:"key_ops,omitempty"`
	// Alg is the key's own "alg" member, the family's default (RS256, ES256, EdDSA) when empty;
	// OmitAlg leaves it out. A value that differs from the token's alg makes the key unusable
	// for that token.
	Alg     string `json:"alg,omitempty"`
	OmitAlg bool   `json:"omit_alg,omitempty"`
	// Unpublished keeps the key out of the JWKS: it can sign, but no verifier can find it.
	Unpublished bool `json:"unpublished,omitempty"`
}

// Key is a signing key of the double.
type Key struct {
	// KID is the key ID the JWKS carries, empty for a key published without one.
	KID string
	// Type is the key's kind.
	Type KeyType

	opts   KeyOptions
	signer crypto.Signer
}

// Info is the key's metadata as the control API shows it.
type Info struct {
	KID       string  `json:"kid"`
	Type      KeyType `json:"type"`
	Published bool    `json:"published"`
}

func newKey(o KeyOptions) (*Key, error) {
	if o.Type == "" {
		o.Type = RSA2048
	}
	var signer crypto.Signer
	var err error
	switch o.Type {
	case RSA2048:
		signer, err = rsa.GenerateKey(rand.Reader, 2048)
	case RSA4096:
		signer, err = rsa.GenerateKey(rand.Reader, 4096)
	case RSA1024:
		signer, err = rsa.GenerateKey(rand.Reader, 1024) //nolint:gosec // the weak key is the point: R-CI-003 refuses RSA keys below 2048 bits
	case ES256:
		signer, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	case EdDSA:
		_, signer, err = ed25519.GenerateKey(rand.Reader)
	default:
		return nil, fmt.Errorf("oidcstub: unknown key type %q", o.Type)
	}
	if err != nil {
		return nil, err
	}
	k := &Key{Type: o.Type, opts: o, signer: signer}
	if !o.NoKID {
		k.KID = o.KID
		if k.KID == "" {
			k.KID = k.thumbprint()
		}
	}
	return k, nil
}

// DefaultAlg is the JOSE algorithm a key of this type signs with by default.
func (t KeyType) DefaultAlg() string {
	switch t {
	case ES256:
		return "ES256"
	case EdDSA:
		return "EdDSA"
	}
	return "RS256"
}

// Public returns the public half.
func (k *Key) Public() crypto.PublicKey { return k.signer.Public() }

// PublicKeyPEM is the PKIX PEM encoding of the public key.
func (k *Key) PublicKeyPEM() []byte {
	der, _ := x509.MarshalPKIXPublicKey(k.signer.Public())
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
}

// Published reports whether the JWKS lists the key.
func (k *Key) Published() bool { return !k.opts.Unpublished }

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// core is the key's required JWK members (RFC 7638 §3.2).
func (k *Key) core() map[string]any {
	switch pub := k.signer.Public().(type) {
	case *rsa.PublicKey:
		return map[string]any{"kty": "RSA", "n": b64(pub.N.Bytes()), "e": b64(big.NewInt(int64(pub.E)).Bytes())}
	case *ecdsa.PublicKey:
		raw, _ := pub.Bytes() // 0x04 || X || Y
		return map[string]any{"kty": "EC", "crv": "P-256", "x": b64(raw[1:33]), "y": b64(raw[33:])}
	case ed25519.PublicKey:
		return map[string]any{"kty": "OKP", "crv": "Ed25519", "x": b64(pub)}
	}
	return nil
}

func (k *Key) thumbprint() string {
	raw, _ := json.Marshal(k.core()) // sorted members, no whitespace
	sum := sha256.Sum256(raw)
	return b64(sum[:])
}

// JWK is the key's JWKS entry.
func (k *Key) JWK() map[string]any {
	jwk := k.core()
	if k.KID != "" {
		jwk["kid"] = k.KID
	}
	if !k.opts.OmitUse {
		use := k.opts.Use
		if use == "" {
			use = "sig"
		}
		jwk["use"] = use
	}
	if !k.opts.OmitAlg {
		alg := k.opts.Alg
		if alg == "" {
			alg = k.Type.DefaultAlg()
		}
		jwk["alg"] = alg
	}
	if k.opts.KeyOps != nil {
		jwk["key_ops"] = k.opts.KeyOps
	}
	return jwk
}

func (k *Key) info() Info { return Info{KID: k.KID, Type: k.Type, Published: k.Published()} }
