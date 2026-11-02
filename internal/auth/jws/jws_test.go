package jws_test

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/jws"
	"github.com/guided-traffic/trove/test/oidcstub"
)

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// signed builds a compact JWS with the header and payload given, signed by priv under alg. The
// production code never signs; this is the test side of the verifier.
func signed(t testing.TB, alg string, priv crypto.Signer, header map[string]any, payload string) string {
	t.Helper()
	if header == nil {
		header = map[string]any{"alg": alg, "typ": "JWT", "kid": "k"}
	}
	hb, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	input := b64(hb) + "." + b64([]byte(payload))
	return input + "." + b64(signature(t, alg, priv, []byte(input)))
}

func signature(t testing.TB, alg string, priv crypto.Signer, input []byte) []byte {
	t.Helper()
	var h crypto.Hash
	switch alg[2:] {
	case "256":
		h = crypto.SHA256
	case "384":
		h = crypto.SHA384
	case "512":
		h = crypto.SHA512
	}
	var digest []byte
	switch h {
	case crypto.SHA256:
		s := sha256.Sum256(input)
		digest = s[:]
	case crypto.SHA384:
		s := sha512.Sum384(input)
		digest = s[:]
	case crypto.SHA512:
		s := sha512.Sum512(input)
		digest = s[:]
	}
	switch k := priv.(type) {
	case *rsa.PrivateKey:
		var sig []byte
		var err error
		if strings.HasPrefix(alg, "PS") {
			sig, err = rsa.SignPSS(rand.Reader, k, h, digest, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
		} else {
			sig, err = rsa.SignPKCS1v15(rand.Reader, k, h, digest)
		}
		if err != nil {
			t.Fatal(err)
		}
		return sig
	case *ecdsa.PrivateKey:
		r, s, err := ecdsa.Sign(rand.Reader, k, digest)
		if err != nil {
			t.Fatal(err)
		}
		n := (k.Curve.Params().BitSize + 7) / 8
		out := make([]byte, 2*n)
		r.FillBytes(out[:n])
		s.FillBytes(out[n:])
		return out
	case ed25519.PrivateKey:
		return ed25519.Sign(k, input)
	}
	t.Fatalf("no signer for %T", priv)
	return nil
}

func rsaKey(t testing.TB, bits int) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func ecKey(t testing.TB, c elliptic.Curve) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(c, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func edKey(t testing.TB) ed25519.PrivateKey {
	t.Helper()
	_, k, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// jwkOf is the JWK of a public key with extra members.
func jwkOf(pub crypto.PublicKey, extra map[string]any) map[string]any {
	var m map[string]any
	switch k := pub.(type) {
	case *rsa.PublicKey:
		m = map[string]any{"kty": "RSA", "n": b64(k.N.Bytes()), "e": b64(big.NewInt(int64(k.E)).Bytes())}
	case *ecdsa.PublicKey:
		raw, _ := k.Bytes()
		n := (len(raw) - 1) / 2
		crv := map[int]string{32: "P-256", 48: "P-384", 66: "P-521"}[n]
		m = map[string]any{"kty": "EC", "crv": crv, "x": b64(raw[1 : 1+n]), "y": b64(raw[1+n:])}
	case ed25519.PublicKey:
		m = map[string]any{"kty": "OKP", "crv": "Ed25519", "x": b64(k)}
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func set(t testing.TB, keys ...map[string]any) jws.Set {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"keys": keys})
	s, err := jws.ParseJWKS(body)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestR_CI_004_EverySupportedAlgorithmVerifies(t *testing.T) {
	r := rsaKey(t, 2048)
	cases := []struct {
		alg  string
		priv crypto.Signer
	}{
		{"RS256", r},
		{"RS384", r},
		{"RS512", r},
		{"PS256", r},
		{"PS384", r},
		{"PS512", r},
		{"ES256", ecKey(t, elliptic.P256())},
		{"ES384", ecKey(t, elliptic.P384())},
		{"EdDSA", edKey(t)},
	}
	if got := len(cases); got != len(jws.Algorithms()) {
		t.Fatalf("%d cases for %d algorithms", got, len(jws.Algorithms()))
	}
	for _, c := range cases {
		t.Run("R-CI-004 "+c.alg+" verifies with its key and not with another", func(t *testing.T) {
			tok, err := jws.Parse(signed(t, c.alg, c.priv, nil, `{"sub":"x"}`))
			if err != nil {
				t.Fatal(err)
			}
			if err := tok.VerifyWith(c.priv.Public(), c.alg); err != nil {
				t.Fatalf("own key: %v", err)
			}
			if err := tok.VerifyWith(c.priv.Public(), ""); err != nil {
				t.Fatalf("own key without alg member: %v", err)
			}
			other := map[string]crypto.Signer{"RSA": rsaKey(t, 2048), "EC": ecKey(t, elliptic.P256()), "OKP": edKey(t)}
			kty, _ := jws.KeyType(c.alg)
			if strings.HasPrefix(c.alg, "ES384") {
				other["EC"] = ecKey(t, elliptic.P384())
			}
			if err := tok.VerifyWith(other[kty].Public(), ""); !errors.Is(err, jws.ErrSignature) {
				t.Fatalf("a foreign key of the same type: %v, want ErrSignature", err)
			}
		})
	}
}

func TestR_CI_004_NoneAndHMACAreNeverSupported(t *testing.T) {
	stub, err := oidcstub.New(oidcstub.Config{PublicURL: "https://gitlab.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	// The double publishes GitLab 19.0.1's two RSA keys and signs with the newer one (test/oidcstub/testdata/issuer.json).
	pub := stub.Keys()
	if len(pub) != 2 {
		t.Fatalf("stub keys %v", pub)
	}
	rsaPub := mustRSAPublic(t, stub)
	for _, alg := range []string{"HS256", "HS384", "HS512", "none", "None", "NONE", "RSA-OAEP", ""} {
		if jws.Supported(alg) {
			t.Errorf("%q is supported", alg)
		}
	}
	t.Run("R-CI-004 HS256 signed with the PEM of the RSA public key does not verify with that key (Q-CI-24)", func(t *testing.T) {
		raw, err := stub.Mint(oidcstub.MintOptions{Alg: "HS256"})
		if err != nil {
			t.Fatal(err)
		}
		tok, err := jws.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if err := tok.VerifyWith(rsaPub, ""); !errors.Is(err, jws.ErrAlgorithm) {
			t.Fatalf("VerifyWith = %v, want ErrAlgorithm", err)
		}
	})
	t.Run("R-CI-004 an unsecured token parses as unsecured and never verifies", func(t *testing.T) {
		raw, err := stub.Mint(oidcstub.MintOptions{Alg: "none"})
		if err != nil {
			t.Fatal(err)
		}
		tok, err := jws.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if !tok.Unsecured() {
			t.Fatal("not unsecured")
		}
		if err := tok.VerifyWith(rsaPub, ""); err == nil {
			t.Fatal("an unsecured token verified")
		}
	})
}

// mustRSAPublic reads the stub's RSA public key from its JWKS answer.
func mustRSAPublic(t *testing.T, stub *oidcstub.Server) *rsa.PublicKey {
	t.Helper()
	// The signing key is the newest one, the last of the set.
	s := stubSet(t, stub)
	for i := len(s.Keys) - 1; i >= 0; i-- {
		if p, ok := s.Keys[i].Public.(*rsa.PublicKey); ok {
			return p
		}
	}
	t.Fatal("no RSA key")
	return nil
}

func TestR_CI_004_TheParserRefusesEveryMalformedShape(t *testing.T) {
	r := rsaKey(t, 2048)
	good := signed(t, "RS256", r, nil, `{"marker":"SECRET-MARKER"}`)
	parts := strings.Split(good, ".")
	header := func(h string) string { return b64([]byte(h)) + "." + parts[1] + "." + parts[2] }
	long := strings.Repeat("k", jws.MaxKIDLength+1)
	cases := []struct{ name, token string }{
		{"five parts (a compact JWE)", strings.Join(append(parts, "a", "b"), ".")},
		{"two parts", parts[0] + "." + parts[1]},
		{"four parts", good + ".x"},
		{"a line break inside a part", parts[0] + "\n." + parts[1] + "." + parts[2]},
		{"padding", parts[0] + "=." + parts[1] + "." + parts[2]},
		{"a character outside base64url", parts[0] + "." + parts[1] + "+." + parts[2]},
		{"a crit header", header(`{"alg":"RS256","crit":["exp"]}`)},
		{"a duplicate header member", header(`{"alg":"RS256","alg":"none"}`)},
		{"a header that is not an object", header(`["RS256"]`)},
		{"a header that is not JSON", header(`alg=RS256`)},
		{"a header without alg", header(`{"kid":"k"}`)},
		{"an alg that is not a string", header(`{"alg":256}`)},
		{"a kid that is not a string", header(`{"alg":"RS256","kid":7}`)},
		{"a kid over 256 characters", header(`{"alg":"RS256","kid":"` + long + `"}`)},
		{"a typ that is not a string", header(`{"alg":"RS256","typ":true}`)},
		{"a header that is not UTF-8", b64([]byte{'{', '"', 'a', '"', ':', '"', 0xff, '"', '}'}) + "." + parts[1] + "." + parts[2]},
	}
	for _, c := range cases {
		t.Run("R-CI-004 "+c.name+" is malformed", func(t *testing.T) {
			_, err := jws.Parse(c.token)
			if !errors.Is(err, jws.ErrMalformed) {
				t.Fatalf("Parse = %v, want ErrMalformed", err)
			}
			if strings.Contains(err.Error(), "SECRET-MARKER") || strings.Contains(err.Error(), parts[1]) {
				t.Fatalf("the error holds token material: %v", err)
			}
		})
	}
	t.Run("R-CI-004 a kid of exactly 256 characters parses", func(t *testing.T) {
		if _, err := jws.Parse(header(`{"alg":"RS256","kid":"` + long[1:] + `"}`)); err != nil {
			t.Fatal(err)
		}
	})
}

func TestR_CI_004_NoKeyComesFromATokenHeader(t *testing.T) {
	published := rsaKey(t, 2048)
	attacker := rsaKey(t, 2048)
	header := map[string]any{
		"alg": "RS256", "kid": "published",
		"jwk": jwkOf(attacker.Public(), nil), "jku": "https://attacker.example/keys",
		"x5u": "https://attacker.example/cert", "x5c": []string{"MIIB"},
	}
	tok, err := jws.Parse(signed(t, "RS256", attacker, header, `{"sub":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if tok.Header != (jws.Header{Alg: "RS256", Kid: "published"}) {
		t.Fatalf("header = %+v: only alg, kid, typ and cty are read", tok.Header)
	}
	if err := tok.VerifyWith(published.Public(), "RS256"); !errors.Is(err, jws.ErrSignature) {
		t.Fatalf("the published key verified a token signed with the header's key: %v", err)
	}
}

func TestTheKeyRulesOfAJWKSet(t *testing.T) {
	r := rsaKey(t, 2048)
	small := rsaKey(t, 1024)
	p256 := ecKey(t, elliptic.P256())
	p521 := ecKey(t, elliptic.P521())
	ed := edKey(t)
	s := set(t,
		jwkOf(r.Public(), map[string]any{"kid": "ok-rsa", "use": "sig", "alg": "RS256"}),
		jwkOf(r.Public(), map[string]any{"kid": "ok-verify", "key_ops": []string{"verify"}}),
		jwkOf(p256.Public(), map[string]any{"kid": "ok-ec", "alg": "ES256"}),
		jwkOf(ed.Public(), map[string]any{"kid": "ok-ed", "alg": "EdDSA"}),
		jwkOf(r.Public(), map[string]any{"kid": "enc", "use": "enc"}),
		jwkOf(r.Public(), map[string]any{"kid": "sign-only", "key_ops": []string{"sign"}}),
		jwkOf(r.Public(), map[string]any{"kid": "no-ops", "key_ops": []string{}}),
		jwkOf(small.Public(), map[string]any{"kid": "small"}),
		jwkOf(p521.Public(), map[string]any{"kid": "p521"}),
		map[string]any{"kty": "oct", "kid": "oct", "k": "c2VjcmV0"},
		jwkOf(r.Public(), map[string]any{"kid": "private", "d": "AQAB"}),
		jwkOf(r.Public(), map[string]any{"kid": "mismatch", "alg": "ES256"}),
		jwkOf(p256.Public(), map[string]any{"kid": "curve-mismatch", "alg": "ES384"}),
		jwkOf(r.Public(), map[string]any{"kid": "hmac", "alg": "HS256"}),
		map[string]any{"kty": "RSA", "kid": "bad-n", "n": "!!", "e": "AQAB"},
		map[string]any{"kty": "EC", "kid": "off-curve", "crv": "P-256", "x": b64(make([]byte, 32)), "y": b64(make([]byte, 32))},
		map[string]any{"kty": "RSA", "kid": "use-number", "use": 1},
	)
	var usable []string
	for _, k := range s.Keys {
		usable = append(usable, k.KID)
	}
	if strings.Join(usable, ",") != "ok-rsa,ok-verify,ok-ec,ok-ed" {
		t.Fatalf("usable keys %v", usable)
	}
	want := map[string]string{
		"enc": jws.IgnoredUse, "sign-only": jws.IgnoredKeyOps, "no-ops": jws.IgnoredKeyOps,
		"small": jws.IgnoredRSASize, "p521": jws.IgnoredCurve, "oct": jws.IgnoredPrivate,
		"private": jws.IgnoredPrivate, "mismatch": jws.IgnoredAlgorithmMismatch,
		"curve-mismatch": jws.IgnoredAlgorithmMismatch, "hmac": jws.IgnoredAlgorithm,
		"bad-n": jws.IgnoredInvalid, "off-curve": jws.IgnoredInvalid, "use-number": jws.IgnoredInvalid,
	}
	if len(s.Ignored) != len(want) {
		t.Fatalf("ignored %+v", s.Ignored)
	}
	for _, ig := range s.Ignored {
		t.Run("the key "+ig.KID+" is ignored as "+want[ig.KID], func(t *testing.T) {
			if ig.Reason != want[ig.KID] {
				t.Fatalf("reason %q, want %q", ig.Reason, want[ig.KID])
			}
		})
	}
	t.Run("an oct key without private material is an unsupported type", func(t *testing.T) {
		s := set(t, map[string]any{"kty": "oct", "kid": "o"})
		if len(s.Ignored) != 1 || s.Ignored[0].Reason != jws.IgnoredKeyType {
			t.Fatalf("%+v", s.Ignored)
		}
	})
	t.Run("a body that is not a JWK set is an error, a set without keys is empty", func(t *testing.T) {
		if _, err := jws.ParseJWKS([]byte("<html>")); err == nil {
			t.Fatal("HTML parsed")
		}
		s, err := jws.ParseJWKS([]byte(`{}`))
		if err != nil || len(s.Keys)+len(s.Ignored) != 0 {
			t.Fatalf("%+v %v", s, err)
		}
	})
}

func TestR_CI_004_AKeyVerifiesOnlyItsAlgorithmFamily(t *testing.T) {
	r := rsaKey(t, 2048)
	small := rsaKey(t, 1024)
	p256 := ecKey(t, elliptic.P256())
	p384 := ecKey(t, elliptic.P384())
	ed := edKey(t)
	cases := []struct {
		name   string
		alg    string
		signer crypto.Signer
		pub    crypto.PublicKey
		keyAlg string
	}{
		{"an RS256 token and an EC key", "RS256", r, p256.Public(), ""},
		{"an ES256 token and an RSA key", "ES256", p256, r.Public(), ""},
		{"an ES256 token and a P-384 key", "ES256", p256, p384.Public(), ""},
		{"an ES384 token and a P-256 key", "ES384", p384, p256.Public(), ""},
		{"an EdDSA token and an RSA key", "EdDSA", ed, r.Public(), ""},
		{"an RS256 token and a key whose own alg is RS384", "RS256", r, r.Public(), "RS384"},
		{"an RS256 token and a 1024-bit RSA key", "RS256", small, small.Public(), ""},
		{"an EdDSA token and a truncated Ed25519 key", "EdDSA", ed, ed.Public().(ed25519.PublicKey)[:16], ""},
	}
	for _, c := range cases {
		t.Run("R-CI-004 "+c.name+" do not fit", func(t *testing.T) {
			tok, err := jws.Parse(signed(t, c.alg, c.signer, nil, `{}`))
			if err != nil {
				t.Fatal(err)
			}
			if jws.Fits(c.alg, c.pub, c.keyAlg) {
				t.Fatal("Fits")
			}
			if err := tok.VerifyWith(c.pub, c.keyAlg); !errors.Is(err, jws.ErrKey) {
				t.Fatalf("VerifyWith = %v, want ErrKey", err)
			}
		})
	}
}

func TestR_CI_004_DecodeObjectRefusesWhatTwoParsersWouldReadDifferently(t *testing.T) {
	for name, body := range map[string]string{
		"a duplicate top-level member":     `{"iss":"a","iss":"b"}`,
		"a duplicate nested member":        `{"job":{"url":"a","url":"b"}}`,
		"a duplicate after an escape":      `{"iss":"a","iss":"b"}`,
		"an array":                         `["iss"]`,
		"null":                             `null`,
		"trailing data":                    `{"iss":"a"}{"iss":"b"}`,
		"invalid UTF-8":                    "{\"iss\":\"\xc3\x28\"}",
		"nesting deeper than 64":           strings.Repeat(`{"a":`, 66) + `1` + strings.Repeat(`}`, 66),
		"an unterminated object":           `{"iss":"a"`,
		"a duplicate inside an array item": `{"l":[{"a":1,"a":2}]}`,
	} {
		t.Run("R-CI-004 "+name+" is malformed", func(t *testing.T) {
			if _, err := jws.DecodeObject([]byte(body)); !errors.Is(err, jws.ErrMalformed) {
				t.Fatalf("DecodeObject = %v", err)
			}
		})
	}
	t.Run("R-CI-004 numbers stay json.Number", func(t *testing.T) {
		m, err := jws.DecodeObject([]byte(`{"exp":1792940000.5,"n":[1,{"x":null}]}`))
		if err != nil {
			t.Fatal(err)
		}
		if m["exp"] != json.Number("1792940000.5") {
			t.Fatalf("%#v", m["exp"])
		}
	})
}

func TestR_CI_004_KeySelection(t *testing.T) {
	r1, r2 := rsaKey(t, 2048), rsaKey(t, 2048)
	ec := ecKey(t, elliptic.P256())
	s := set(t,
		jwkOf(r1.Public(), map[string]any{"kid": "a"}),
		jwkOf(r2.Public(), map[string]any{"kid": "b", "alg": "RS256"}),
		jwkOf(ec.Public(), map[string]any{"kid": "c"}),
	)
	t.Run("R-AUTH-006 the login selects by kid, and without kid only a single fitting key", func(t *testing.T) {
		if k := jws.SelectKey(s.Keys, "b", "RS256"); k == nil || k.KID != "b" {
			t.Fatalf("%+v", k)
		}
		if k := jws.SelectKey(s.Keys, "c", "RS256"); k != nil {
			t.Fatal("an EC key selected for RS256")
		}
		if k := jws.SelectKey(s.Keys, "", "RS256"); k != nil {
			t.Fatal("two RSA keys without kid selected one")
		}
		if k := jws.SelectKey(s.Keys, "", "ES256"); k == nil || k.KID != "c" {
			t.Fatalf("%+v", k)
		}
	})
	t.Run("R-CI-004 the exchange's candidates fit the algorithm and are bounded", func(t *testing.T) {
		if got := jws.Candidates(s.Keys, "", "RS256", 10); len(got) != 2 {
			t.Fatalf("%d candidates", len(got))
		}
		if got := jws.Candidates(s.Keys, "", "RS256", 1); len(got) != 1 || got[0].KID != "a" {
			t.Fatalf("%+v", got)
		}
		if got := jws.Candidates(s.Keys, "b", "RS384", 10); len(got) != 0 {
			t.Fatal("a key whose alg is RS256 is a candidate for RS384")
		}
		if got := jws.Candidates(s.Keys, "c", "ES256", 10); len(got) != 1 {
			t.Fatalf("%+v", got)
		}
	})
}

// stubSet parses the issuer double's key set as the key store would: the body its key path serves,
// asked in process.
func stubSet(t testing.TB, stub *oidcstub.Server) jws.Set {
	t.Helper()
	rec := httptest.NewRecorder()
	stub.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, oidcstub.JWKSPath, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("JWKS: status %d", rec.Code)
	}
	s, err := jws.ParseJWKS(rec.Body.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	return s
}
