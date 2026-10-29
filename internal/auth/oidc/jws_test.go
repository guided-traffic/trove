package oidc

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"strings"
	"testing"
)

// signer signs test tokens. It is the test side of the verifier: the
// production code never signs.
type signer struct {
	kid string
	alg string
	rsa *rsa.PrivateKey
	ec  *ecdsa.PrivateKey
}

func newRSASigner(t testing.TB, kid, alg string) *signer {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return &signer{kid: kid, alg: alg, rsa: k}
}

func newECSigner(t testing.TB, kid string) *signer {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &signer{kid: kid, alg: "ES256", ec: k}
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func (s *signer) jwk() map[string]any {
	if s.rsa != nil {
		return map[string]any{
			"kty": "RSA", "use": "sig", "kid": s.kid, "alg": s.alg,
			"n": b64(s.rsa.N.Bytes()), "e": b64(big.NewInt(int64(s.rsa.E)).Bytes()),
		}
	}
	pad := func(b []byte) []byte { return append(make([]byte, 32-len(b)), b...) }
	return map[string]any{
		"kty": "EC", "use": "sig", "kid": s.kid, "crv": "P-256",
		"x": b64(pad(s.ec.X.Bytes())), "y": b64(pad(s.ec.Y.Bytes())),
	} //nolint:staticcheck // test key material
}

func (s *signer) sign(t testing.TB, header map[string]any, payload any) string {
	t.Helper()
	if header == nil {
		header = map[string]any{"alg": s.alg, "kid": s.kid, "typ": "JWT"}
	}
	hb, _ := json.Marshal(header)
	pb, _ := json.Marshal(payload)
	input := b64(hb) + "." + b64(pb)
	sum := sha256.Sum256([]byte(input))
	var sig []byte
	var err error
	switch {
	case s.rsa != nil && strings.HasPrefix(s.alg, "PS"):
		sig, err = rsa.SignPSS(rand.Reader, s.rsa, crypto.SHA256, sum[:], &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
	case s.rsa != nil:
		sig, err = rsa.SignPKCS1v15(rand.Reader, s.rsa, crypto.SHA256, sum[:])
	default:
		var r, ss *big.Int
		r, ss, err = ecdsa.Sign(rand.Reader, s.ec, sum[:])
		if err == nil {
			sig = append(append(make([]byte, 32-len(r.Bytes())), r.Bytes()...), append(make([]byte, 32-len(ss.Bytes())), ss.Bytes()...)...)
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	return input + "." + b64(sig)
}

func keysOf(t *testing.T, signers ...*signer) []*jwk {
	t.Helper()
	var set []map[string]any
	for _, s := range signers {
		set = append(set, s.jwk())
	}
	body, _ := json.Marshal(map[string]any{"keys": set})
	keys, err := parseJWKS(body)
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

func TestR_AUTH_006_JWSVerification(t *testing.T) {
	all := []string{"RS256", "PS256", "ES256"}
	rs := newRSASigner(t, "rs", "RS256")
	ps := newRSASigner(t, "ps", "PS256")
	es := newECSigner(t, "es")
	keys := keysOf(t, rs, ps, es)
	payload := map[string]any{"sub": "alice"}

	for _, s := range []*signer{rs, ps, es} {
		t.Run("R-AUTH-006 a token signed with "+s.alg+" verifies against its JWKS key", func(t *testing.T) {
			j, err := parseJWS(s.sign(t, nil, payload))
			if err != nil {
				t.Fatal(err)
			}
			k := selectKey(keys, j.header.Kid, j.header.Alg)
			if k == nil {
				t.Fatal("no key")
			}
			if err := verify(j, k, all); err != nil {
				t.Fatalf("verify: %v", err)
			}
		})
	}

	t.Run("R-AUTH-006 alg none is refused", func(t *testing.T) {
		hb, _ := json.Marshal(map[string]any{"alg": "none", "kid": "rs"})
		pb, _ := json.Marshal(payload)
		j, err := parseJWS(b64(hb) + "." + b64(pb) + ".")
		if err != nil {
			t.Fatal(err)
		}
		if err := verify(j, keys[0], append(all, "none")); err == nil {
			t.Fatal("alg none verified")
		}
	})

	t.Run("R-AUTH-006 HS256 with the RSA modulus as the HMAC key is refused (key confusion)", func(t *testing.T) {
		hb, _ := json.Marshal(map[string]any{"alg": "HS256", "kid": "rs"})
		pb, _ := json.Marshal(payload)
		input := b64(hb) + "." + b64(pb)
		mac := hmac.New(sha256.New, rs.rsa.N.Bytes())
		mac.Write([]byte(input))
		j, err := parseJWS(input + "." + b64(mac.Sum(nil)))
		if err != nil {
			t.Fatal(err)
		}
		if err := verify(j, selectKey(keys, "rs", "RS256"), append(all, "HS256")); err == nil {
			t.Fatal("HS256 verified")
		}
	})

	t.Run("R-AUTH-006 a token signed with an unpublished key does not verify", func(t *testing.T) {
		rogue := newRSASigner(t, "rs", "RS256")
		j, _ := parseJWS(rogue.sign(t, nil, payload))
		if err := verify(j, selectKey(keys, "rs", "RS256"), all); err == nil {
			t.Fatal("a foreign signature verified")
		}
	})

	t.Run("R-AUTH-006 an algorithm outside allowed_signing_algs is refused", func(t *testing.T) {
		j, _ := parseJWS(ps.sign(t, nil, payload))
		if err := verify(j, selectKey(keys, "ps", "PS256"), []string{"RS256"}); err == nil {
			t.Fatal("PS256 verified although only RS256 is allowed")
		}
	})

	t.Run("R-AUTH-006 an encrypted ID token (five parts) and a crit header are refused", func(t *testing.T) {
		if _, err := parseJWS("a.b.c.d.e"); err == nil {
			t.Fatal("a JWE parsed")
		}
		tok := rs.sign(t, map[string]any{"alg": "RS256", "kid": "rs", "crit": []string{"exp"}}, payload)
		if _, err := parseJWS(tok); err == nil {
			t.Fatal("crit was accepted")
		}
	})

	t.Run("R-AUTH-006 an EC signature is not accepted by an RSA key of the same kid", func(t *testing.T) {
		j, _ := parseJWS(es.sign(t, map[string]any{"alg": "ES256", "kid": "rs"}, payload))
		if k := selectKey(keys, "rs", "ES256"); k != nil {
			if err := verify(j, k, all); err == nil {
				t.Fatal("verified with the wrong key type")
			}
		}
	})

	t.Run("R-AUTH-006 a JWKS key whose use is enc and an RSA key below 2048 bits are left out", func(t *testing.T) {
		small, _ := rsa.GenerateKey(rand.Reader, 1024)
		body, _ := json.Marshal(map[string]any{"keys": []map[string]any{
			{"kty": "RSA", "use": "enc", "kid": "e", "n": b64(rs.rsa.N.Bytes()), "e": "AQAB"},
			{"kty": "RSA", "kid": "s", "n": b64(small.N.Bytes()), "e": "AQAB"},
		}})
		keys, err := parseJWKS(body)
		if err != nil || len(keys) != 0 {
			t.Fatalf("keys = %d, err %v", len(keys), err)
		}
	})
}
