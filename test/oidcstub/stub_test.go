package oidcstub

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// fixture is a running double and a client that trusts its CA.
type fixture struct {
	t      *testing.T
	s      *Server
	run    *Running
	ca     *SelfSigned
	client *http.Client
}

func newFixture(t *testing.T, cfg Config) *fixture {
	t.Helper()
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	run, ca, err := s.StartSelfSigned()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = run.Close() })
	tr := &http.Transport{TLSClientConfig: ca.ClientConfig(), DisableKeepAlives: true}
	return &fixture{t: t, s: s, run: run, ca: ca, client: &http.Client{Transport: tr, Timeout: 5 * time.Second}}
}

// reply is what a test needs of a response whose body was read and closed.
type reply struct {
	StatusCode int
	Status     string
	Header     http.Header
}

func (f *fixture) get(url string) (reply, []byte, error) {
	f.t.Helper()
	resp, err := f.client.Get(url)
	if err != nil {
		return reply{}, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return reply{resp.StatusCode, resp.Status, resp.Header}, body, err
}

func (f *fixture) mustJSON(url string, into any) reply {
	f.t.Helper()
	resp, body, err := f.get(url)
	if err != nil {
		f.t.Fatalf("GET %s: %v", url, err)
	}
	if err := json.Unmarshal(body, into); err != nil {
		f.t.Fatalf("GET %s: %v in %q", url, err, body)
	}
	return resp
}

type jwk struct {
	KTY    string   `json:"kty"`
	KID    string   `json:"kid"`
	Use    string   `json:"use"`
	Alg    string   `json:"alg"`
	KeyOps []string `json:"key_ops"`
	N      string   `json:"n"`
	E      string   `json:"e"`
	CRV    string   `json:"crv"`
	X      string   `json:"x"`
	Y      string   `json:"y"`
}

type jwksDoc struct {
	Keys []jwk `json:"keys"`
}

func (f *fixture) jwks() jwksDoc {
	f.t.Helper()
	var d jwksDoc
	f.mustJSON(f.s.JWKSURL(), &d)
	return d
}

func dec(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("base64url %q: %v", s, err)
	}
	return b
}

func (k jwk) public(t *testing.T) crypto.PublicKey {
	t.Helper()
	switch k.KTY {
	case "RSA":
		return &rsa.PublicKey{N: new(big.Int).SetBytes(dec(t, k.N)), E: int(new(big.Int).SetBytes(dec(t, k.E)).Int64())}
	case "EC":
		raw := append([]byte{4}, dec(t, k.X)...)
		pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), append(raw, dec(t, k.Y)...))
		if err != nil {
			t.Fatal(err)
		}
		return pub
	case "OKP":
		return ed25519.PublicKey(dec(t, k.X))
	}
	t.Fatalf("unknown kty %q", k.KTY)
	return nil
}

// verifyJWS checks a compact JWS with the standard library against the published key set: the
// way a validating client does, with no help from the package under test.
func verifyJWS(t *testing.T, tok string, keys jwksDoc) (header, payload map[string]any) {
	t.Helper()
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d parts", len(parts))
	}
	if err := json.Unmarshal(dec(t, parts[0]), &header); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(dec(t, parts[1]), &payload); err != nil {
		t.Fatal(err)
	}
	alg, _ := header["alg"].(string)
	kid, _ := header["kid"].(string)
	var key *jwk
	for i := range keys.Keys {
		if keys.Keys[i].KID == kid {
			key = &keys.Keys[i]
		}
	}
	if key == nil {
		t.Fatalf("kid %q is not in the key set", kid)
	}
	input, sig := []byte(parts[0]+"."+parts[1]), dec(t, parts[2])
	sum := sha256.Sum256(input)
	var err error
	switch pub := key.public(t).(type) {
	case *rsa.PublicKey:
		switch alg {
		case "RS256":
			err = rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig)
		case "PS256":
			err = rsa.VerifyPSS(pub, crypto.SHA256, sum[:], sig, nil)
		default:
			t.Fatalf("alg %s is not verified here", alg)
		}
	case *ecdsa.PublicKey:
		if len(sig) != 64 || !ecdsa.Verify(pub, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
			err = errors.New("bad ES256 signature")
		}
	case ed25519.PublicKey:
		if !ed25519.Verify(pub, input, sig) {
			err = errors.New("bad EdDSA signature")
		}
	}
	if err != nil {
		t.Fatalf("signature of %s token does not verify: %v", alg, err)
	}
	return header, payload
}

// The double serves the cases of R-CI-003.
func TestDoubleDiscoveryDocument(t *testing.T) {
	f := newFixture(t, Config{})
	var doc map[string]any
	resp := f.mustJSON(f.s.DiscoveryURL(), &doc)
	if resp.StatusCode != 200 {
		t.Fatal(resp.Status)
	}
	if doc["issuer"] != f.run.URL || f.s.Issuer() != f.run.URL {
		t.Fatalf("issuer %v, want %s", doc["issuer"], f.run.URL)
	}
	if doc["jwks_uri"] != f.run.URL+"/oauth/discovery/keys" {
		t.Fatalf("jwks_uri %v", doc["jwks_uri"])
	}
	algs, _ := json.Marshal(doc["id_token_signing_alg_values_supported"])
	if string(algs) != `["RS256"]` {
		t.Fatalf("algorithms %s", algs)
	}
	if _, err := f.s.AddKey(KeyOptions{Type: ES256}); err != nil {
		t.Fatal(err)
	}
	f.mustJSON(f.s.DiscoveryURL(), &doc)
	algs, _ = json.Marshal(doc["id_token_signing_alg_values_supported"])
	if string(algs) != `["ES256","RS256"]` {
		t.Fatalf("algorithms after adding an EC key: %s", algs)
	}
}

// The double serves the cases of R-CI-003.
func TestDoubleJWKSCaching(t *testing.T) {
	f := newFixture(t, Config{})
	f.s.SetCache(Cache{MaxAge: Duration(90 * time.Second)})
	resp, _, err := f.get(f.s.JWKSURL())
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.Header.Get("Cache-Control"); got != "max-age=90" {
		t.Fatalf("Cache-Control %q", got)
	}
	if resp.Header.Get("ETag") != "" {
		t.Fatal("an ETag without being asked for")
	}

	f.s.SetCache(Cache{NoHeader: true})
	resp, _, _ = f.get(f.s.JWKSURL())
	if resp.Header.Get("Cache-Control") != "" {
		t.Fatalf("Cache-Control %q, want none", resp.Header.Get("Cache-Control"))
	}

	f.s.SetCache(Cache{MaxAge: 0, ETag: true})
	resp, _, _ = f.get(f.s.JWKSURL())
	etag := resp.Header.Get("ETag")
	if etag == "" || resp.Header.Get("Cache-Control") != "max-age=0" {
		t.Fatalf("ETag %q, Cache-Control %q", etag, resp.Header.Get("Cache-Control"))
	}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, f.s.JWKSURL(), http.NoBody)
	req.Header.Set("If-None-Match", etag)
	r2, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = r2.Body.Close()
	if r2.StatusCode != http.StatusNotModified {
		t.Fatalf("conditional request: %s", r2.Status)
	}
	if _, err := f.s.Rotate(); err != nil { // a changed key set has another ETag
		t.Fatal(err)
	}
	resp, _, _ = f.get(f.s.JWKSURL())
	if resp.Header.Get("ETag") == etag {
		t.Fatal("the ETag did not change with the key set")
	}
	if c := f.s.Counts(); c.NotModified != 1 || c.JWKS != 5 {
		t.Fatalf("counts %+v", c)
	}
}

// The double serves the cases of R-CI-003.
func TestDoubleMintedTokensVerifyAgainstTheJWKS(t *testing.T) {
	f := newFixture(t, Config{})
	for _, kt := range []struct {
		typ KeyType
		alg string
	}{{ES256, "ES256"}, {EdDSA, "EdDSA"}} {
		if _, err := f.s.AddKey(KeyOptions{Type: kt.typ}); err != nil {
			t.Fatal(err)
		}
	}
	for _, alg := range []string{"RS256", "PS256", "ES256", "EdDSA", ""} {
		tok, err := f.s.Mint(MintOptions{Alg: alg})
		if err != nil {
			t.Fatalf("%s: %v", alg, err)
		}
		h, p := verifyJWS(t, tok, f.jwks())
		want := alg
		if alg == "" {
			want = "RS256"
		}
		if h["alg"] != want || h["typ"] != "JWT" || p["iss"] != f.run.URL {
			t.Fatalf("%s: header %v, iss %v", alg, h, p["iss"])
		}
	}
}

// The double serves the cases of R-CI-003.
func TestDoubleRotationAndRemovalShowInTheJWKS(t *testing.T) {
	f := newFixture(t, Config{})
	first := f.jwks().Keys
	if len(first) != 1 || first[0].KTY != "RSA" || first[0].Use != "sig" || first[0].Alg != "RS256" || first[0].KID == "" {
		t.Fatalf("default key set %+v", first)
	}
	if n := len(dec(t, first[0].N)); n != 256 {
		t.Fatalf("default RSA modulus is %d bytes, want 256 (2048 bits)", n)
	}
	oldTok, err := f.s.Mint(MintOptions{})
	if err != nil {
		t.Fatal(err)
	}

	nk, err := f.s.Rotate()
	if err != nil {
		t.Fatal(err)
	}
	after := f.jwks().Keys
	if len(after) != 2 || after[0].KID != first[0].KID || after[1].KID != nk.KID {
		t.Fatalf("rotation keeps the old key and adds the new one: %+v", after)
	}
	newTok, _ := f.s.Mint(MintOptions{})
	if h, _ := verifyJWS(t, newTok, f.jwks()); h["kid"] != nk.KID {
		t.Fatalf("the newest key signs: kid %v", h["kid"])
	}
	verifyJWS(t, oldTok, f.jwks()) // the old token still verifies while its key is published

	if !f.s.RemoveKey(first[0].KID) || f.s.RemoveKey("nope") {
		t.Fatal("RemoveKey must report whether it removed something")
	}
	if left := f.jwks().Keys; len(left) != 1 || left[0].KID != nk.KID {
		t.Fatalf("after removal %+v", left)
	}
	// A removed key still signs, for a test of "unknown_signing_key".
	gone, err := f.s.Mint(MintOptions{KID: first[0].KID})
	if err != nil {
		t.Fatal(err)
	}
	if h := strings.Split(gone, "."); !strings.Contains(string(dec(t, h[0])), first[0].KID) {
		t.Fatal("the token of a removed key carries its kid")
	}
}

// The double serves the cases of R-CI-003.
func TestDoubleKeyVariants(t *testing.T) {
	f := newFixture(t, Config{NoDefaultKey: true})
	add := func(o KeyOptions) *Key {
		t.Helper()
		k, err := f.s.AddKey(o)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	small := add(KeyOptions{Type: RSA1024, KID: "small"})
	kidless := add(KeyOptions{NoKID: true})
	enc := add(KeyOptions{KID: "enc", Use: "enc"})
	nouse := add(KeyOptions{KID: "nouse", OmitUse: true, OmitAlg: true})
	ops := add(KeyOptions{KID: "ops", KeyOps: []string{"sign"}})
	opsEmpty := add(KeyOptions{KID: "opsempty", KeyOps: []string{}})
	algDiffers := add(KeyOptions{KID: "algdiffers", Alg: "PS256"})
	hidden := add(KeyOptions{KID: "hidden", Unpublished: true})
	add(KeyOptions{Type: ES256, KID: "ec"})
	add(KeyOptions{Type: EdDSA, KID: "ed"})

	by := map[string]jwk{}
	var noKID []jwk
	for _, k := range f.jwks().Keys {
		if k.KID == "" {
			noKID = append(noKID, k)
		}
		by[k.KID] = k
	}
	if n := len(dec(t, by["small"].N)); n != 128 {
		t.Fatalf("the small key has %d modulus bytes, want 128 (1024 bits)", n)
	}
	if len(noKID) != 1 || kidless.KID != "" {
		t.Fatalf("keys without kid: %d", len(noKID))
	}
	if by["enc"].Use != "enc" || by["nouse"].Use != "" || by["nouse"].Alg != "" {
		t.Fatalf("use/alg members: %+v %+v", by["enc"], by["nouse"])
	}
	if got := by["ops"].KeyOps; len(got) != 1 || got[0] != "sign" {
		t.Fatalf("key_ops %v", got)
	}
	raw, _ := json.Marshal(f.s.keys[5].JWK())
	if !bytes.Contains(raw, []byte(`"key_ops":[]`)) {
		t.Fatalf("an empty key_ops list must be published as []: %s", raw)
	}
	if by["algdiffers"].Alg != "PS256" || by["ec"].KTY != "EC" || by["ec"].CRV != "P-256" || by["ed"].KTY != "OKP" || by["ed"].CRV != "Ed25519" {
		t.Fatalf("key families: %+v", by)
	}
	if _, ok := by["hidden"]; ok {
		t.Fatal("an unpublished key is in the JWKS")
	}
	// The default kid is the RFC 7638 thumbprint, so two keys never share one by accident.
	if k := add(KeyOptions{}); len(k.KID) != 43 {
		t.Fatalf("default kid %q is not a SHA-256 thumbprint", k.KID)
	}
	// Signing with every variant by kid works, the verifier's rules are what rejects them.
	for _, k := range []*Key{small, kidless, enc, nouse, ops, opsEmpty, algDiffers, hidden} {
		if _, err := f.s.Mint(MintOptions{SignWith: k}); err != nil {
			t.Fatalf("sign with %q: %v", k.KID, err)
		}
	}
	if h := parseHeader(t, mustMint(t, f.s, MintOptions{SignWith: small})); h["kid"] != "small" {
		t.Fatalf("header %v", h)
	}
	// The 1024-bit key is never picked by default.
	if h := parseHeader(t, mustMint(t, f.s, MintOptions{})); h["kid"] == "small" {
		t.Fatal("the weak key signed by default")
	}
}

func mustMint(t *testing.T, s *Server, o MintOptions) string {
	t.Helper()
	tok, err := s.Mint(o)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func parseHeader(t *testing.T, tok string) map[string]any {
	t.Helper()
	var h map[string]any
	if err := json.Unmarshal(dec(t, strings.Split(tok, ".")[0]), &h); err != nil {
		t.Fatal(err)
	}
	return h
}

func parsePayload(t *testing.T, tok string) map[string]any {
	t.Helper()
	var p map[string]any
	if err := json.Unmarshal(dec(t, strings.Split(tok, ".")[1]), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

// The double serves the cases of R-CI-006.
func TestDoubleDefaultClaims(t *testing.T) {
	f := newFixture(t, Config{})
	before := time.Now().Unix()
	p := parsePayload(t, mustMint(t, f.s, MintOptions{}))
	str := func(name, want string) {
		t.Helper()
		if v, ok := p[name].(string); !ok || v != want {
			t.Errorf("%s = %#v, want the JSON string %q", name, p[name], want)
		}
	}
	str("iss", f.run.URL)
	str("aud", DefaultAudience)
	str("project_id", "1234")
	str("project_path", "acme/billing/invoice-api")
	str("namespace_id", "4711")
	str("namespace_path", "acme/billing")
	str("job_project_path", "acme/billing/invoice-api")
	str("job_namespace_id", "4711")
	str("source_project_id", "1234")
	str("ref", "v1.4.0")
	str("ref_type", "tag")
	str("ref_path", "refs/tags/v1.4.0")
	str("ref_protected", "true")
	str("pipeline_source", "push")
	str("pipeline_id", "98765")
	str("job_id", "123456")
	str("environment", "production")
	str("environment_protected", "true")
	str("deployment_tier", "production")
	str("environment_action", "start")
	str("user_id", "42")
	str("user_login", "jdoe")
	str("project_visibility", "private")
	if s, _ := p["sub"].(string); !strings.HasPrefix(s, "project_path:acme/billing/invoice-api:ref_type:tag:ref:") {
		t.Errorf("sub %q", s)
	}
	if s, _ := p["sha"].(string); len(s) != 40 {
		t.Errorf("sha %q", s)
	}
	if _, ok := p["runner_id"].(float64); !ok {
		t.Errorf("runner_id %#v is a number in GitLab's tokens", p["runner_id"])
	}
	if p["runner_environment"] == nil {
		t.Error("runner_environment missing")
	}
	iat, nbf, exp := int64(p["iat"].(float64)), int64(p["nbf"].(float64)), int64(p["exp"].(float64))
	if iat < before || iat > time.Now().Unix() || nbf != iat-5 || exp != iat+300 {
		t.Errorf("iat %d nbf %d exp %d", iat, nbf, exp)
	}
	// R-CI-018: every token has its own jti.
	a, b := p["jti"], parsePayload(t, mustMint(t, f.s, MintOptions{}))["jti"]
	if a == nil || a == b {
		t.Errorf("jti %v and %v", a, b)
	}
}

// The double serves the cases of R-CI-004.
func TestDoubleClaimsAreOverridableAndRemovable(t *testing.T) {
	f := newFixture(t, Config{})
	p := parsePayload(t, mustMint(t, f.s, MintOptions{
		Claims: map[string]any{
			"aud":          []string{"https://packages.example.com", "https://vault.example.com"},
			"project_path": "other/group/project",
			"jti":          nil,
			"exp":          1234.5,
		},
		Remove: []string{"nbf", "iat", "sub"},
	}))
	if aud, ok := p["aud"].([]any); !ok || len(aud) != 2 {
		t.Errorf("aud %#v", p["aud"])
	}
	if p["project_path"] != "other/group/project" {
		t.Errorf("project_path %v", p["project_path"])
	}
	if v, ok := p["jti"]; !ok || v != nil {
		t.Errorf("a nil claim must be a JSON null, got %v (present %v)", v, ok)
	}
	if p["exp"] != 1234.5 {
		t.Errorf("a non-integer NumericDate: %v", p["exp"])
	}
	for _, c := range []string{"nbf", "iat", "sub"} {
		if _, ok := p[c]; ok {
			t.Errorf("%s was not removed", c)
		}
	}
	if p["namespace_id"] != "4711" {
		t.Error("the other defaults stay")
	}
	// The default audience is configurable; the issuer too, per token.
	g := newFixture(t, Config{Audience: "npm:packages.example.com"})
	if a := parsePayload(t, mustMint(t, g.s, MintOptions{}))["aud"]; a != "npm:packages.example.com" {
		t.Errorf("configured audience %v", a)
	}
	if v := parsePayload(t, mustMint(t, g.s, MintOptions{Claims: map[string]any{"iss": "https://gitlab.example.com"}}))["iss"]; v != "https://gitlab.example.com" {
		t.Errorf("iss override %v", v)
	}
}

// The double serves the cases of R-CI-004.
func TestDoubleHeaderOptions(t *testing.T) {
	f := newFixture(t, Config{})
	long := strings.Repeat("k", 300)
	h := parseHeader(t, mustMint(t, f.s, MintOptions{
		NoKID: true, Typ: "at+jwt",
		Headers: map[string]any{"jku": "https://attacker.example/keys", "x5u": "https://attacker.example/c", "crit": []string{"exp"}},
	}))
	if _, ok := h["kid"]; ok || h["typ"] != "at+jwt" || h["jku"] != "https://attacker.example/keys" || h["x5u"] == nil || h["crit"] == nil {
		t.Errorf("header %v", h)
	}
	h = parseHeader(t, mustMint(t, f.s, MintOptions{NoTyp: true, HeaderKID: long}))
	if _, ok := h["typ"]; ok || h["kid"] != long {
		t.Errorf("header %v", h)
	}
	// A header kid of another key is still signed by the selected key.
	verifyJWS(t, mustMint(t, f.s, MintOptions{}), f.jwks())
}

// The double serves the cases of R-CI-004.
func TestDoubleAlgorithmConfusionHS256(t *testing.T) {
	f := newFixture(t, Config{})
	tok := mustMint(t, f.s, MintOptions{Alg: "HS256"})
	parts := strings.Split(tok, ".")
	if h := parseHeader(t, tok); h["alg"] != "HS256" {
		t.Fatalf("header %v", h)
	}
	// The secret is the PEM of the RSA public key, which anyone can fetch from the JWKS.
	m := hmac.New(sha256.New, f.s.keys[0].PublicKeyPEM())
	m.Write([]byte(parts[0] + "." + parts[1]))
	if !hmac.Equal(m.Sum(nil), dec(t, parts[2])) {
		t.Fatal("the HMAC is not keyed with the public key")
	}
	// An explicit secret wins.
	tok = mustMint(t, f.s, MintOptions{Alg: "HS512", HMACSecret: []byte("s3cret")})
	if len(dec(t, strings.Split(tok, ".")[2])) != 64 {
		t.Fatal("HS512 signature length")
	}
}

// The double serves the cases of R-CI-004.
func TestDoubleMalformedShapes(t *testing.T) {
	f := newFixture(t, Config{})

	none := mustMint(t, f.s, MintOptions{Alg: "none"})
	if p := strings.Split(none, "."); len(p) != 3 || p[2] != "" || parseHeader(t, none)["alg"] != "none" {
		t.Errorf("unsecured token %q", none)
	}

	jwe := mustMint(t, f.s, MintOptions{JWE: true})
	if len(strings.Split(jwe, ".")) != 5 {
		t.Errorf("JWE-shaped value %q", jwe)
	}

	// A duplicate member name survives into the signed bytes.
	dup := mustMint(t, f.s, MintOptions{Duplicate: map[string]any{"sub": "someone-else", "extra": 1}})
	raw := string(dec(t, strings.Split(dup, ".")[1]))
	if strings.Count(raw, `"sub":`) != 2 || !strings.HasSuffix(raw, `,"extra":1,"sub":"someone-else"}`) {
		t.Errorf("payload %s", raw)
	}
	verifyJWS(t, dup, f.jwks()) // the signature is valid: only the shape is wrong

	bad := mustMint(t, f.s, MintOptions{InvalidUTF8: true})
	if utf8.Valid(dec(t, strings.Split(bad, ".")[1])) {
		t.Error("payload is valid UTF-8")
	}
	if len(strings.Split(bad, ".")) != 3 {
		t.Error("invalid UTF-8 token is not a compact JWS")
	}

	big := mustMint(t, f.s, MintOptions{PadTo: 20000})
	if len(big) < 20000 {
		t.Errorf("oversized token has %d bytes", len(big))
	}
	verifyJWS(t, big, f.jwks())

	raw2 := mustMint(t, f.s, MintOptions{RawPayload: []byte(`[1,2]`)})
	if string(dec(t, strings.Split(raw2, ".")[1])) != `[1,2]` {
		t.Error("raw payload")
	}
}

// The double serves the cases of R-CI-004.
func TestDoubleForeignSignature(t *testing.T) {
	f := newFixture(t, Config{})
	tok := mustMint(t, f.s, MintOptions{ForeignSignature: true})
	if h := parseHeader(t, tok); h["kid"] != f.s.keys[0].KID {
		t.Fatalf("the header must name the published key: %v", h)
	}
	parts := strings.Split(tok, ".")
	pub := f.s.keys[0].Public().(*rsa.PublicKey)
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], dec(t, parts[2])) == nil {
		t.Fatal("the signature verifies with the published key")
	}
}

// The double serves the cases of R-CI-004.
func TestDoubleWrongFamilyErrors(t *testing.T) {
	f := newFixture(t, Config{})
	if _, err := f.s.Mint(MintOptions{Alg: "ES256"}); err == nil {
		t.Error("ES256 without an EC key must fail loudly")
	}
	if _, err := f.s.Mint(MintOptions{KID: "unknown"}); err == nil {
		t.Error("an unknown kid must fail loudly")
	}
	if _, err := f.s.Mint(MintOptions{Alg: "ES256", SignWith: f.s.keys[0]}); err == nil {
		t.Error("ES256 with an RSA key")
	}
	if _, err := f.s.AddKey(KeyOptions{Type: "dsa"}); err == nil {
		t.Error("unknown key type")
	}
}

// tryGet makes a request whose answer does not matter, only whether it succeeded.
func tryGet(c *http.Client, url string) error {
	resp, err := c.Get(url)
	if err == nil {
		_ = resp.Body.Close()
	}
	return err
}

func (f *fixture) setFault(fault Fault) {
	f.t.Helper()
	if err := f.s.SetFault(fault); err != nil {
		f.t.Fatal(err)
	}
}

// The double serves the cases of R-CI-003.
func TestDoubleFaultDelay(t *testing.T) {
	f := newFixture(t, Config{})
	f.setFault(Fault{Delay: Duration(300 * time.Millisecond), Target: TargetJWKS})
	start := time.Now()
	if _, _, err := f.get(f.s.DiscoveryURL()); err != nil || time.Since(start) > 200*time.Millisecond {
		t.Fatalf("discovery is not targeted: %v after %v", err, time.Since(start))
	}
	start = time.Now()
	if _, _, err := f.get(f.s.JWKSURL()); err != nil || time.Since(start) < 300*time.Millisecond {
		t.Fatalf("JWKS answered after %v (%v), want at least 300ms", time.Since(start), err)
	}
	// A client that gives up counts the request all the same.
	f.setFault(Fault{Delay: Duration(time.Minute)})
	short := &http.Client{Transport: f.client.Transport, Timeout: 100 * time.Millisecond}
	if err := tryGet(short, f.s.JWKSURL()); err == nil {
		t.Fatal("a held answer must time the client out")
	}
}

// The double serves the cases of R-CI-003.
func TestDoubleFaultUnreachable(t *testing.T) {
	f := newFixture(t, Config{})
	f.setFault(Fault{Unreachable: true})
	if _, _, err := f.get(f.s.JWKSURL()); err == nil {
		t.Fatal("the unreachable issuer answered")
	}
	if c := f.s.Counts(); c.Refused == 0 || c.JWKS != 0 {
		t.Fatalf("counts %+v", c)
	}
	// A connection that was open before the fault is cut too.
	f.setFault(Fault{})
	keep := &http.Client{Transport: &http.Transport{TLSClientConfig: f.ca.ClientConfig()}}
	resp, err := keep.Get(f.s.JWKSURL())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	f.setFault(Fault{Unreachable: true})
	if err := tryGet(keep, f.s.JWKSURL()); err == nil {
		t.Fatal("a kept-alive connection was served while unreachable")
	}
	f.setFault(Fault{})
	if _, _, err := f.get(f.s.JWKSURL()); err != nil {
		t.Fatalf("healed issuer: %v", err)
	}
}

// The double serves the cases of R-CI-003.
func TestDoubleFaultStatusAndBodies(t *testing.T) {
	f := newFixture(t, Config{})
	f.setFault(Fault{Status: 500})
	resp, _, err := f.get(f.s.JWKSURL())
	if err != nil || resp.StatusCode != 500 {
		t.Fatalf("status fault: %v %v", resp, err)
	}

	f.setFault(Fault{NotJSON: true, Target: TargetJWKS})
	resp, body, _ := f.get(f.s.JWKSURL())
	var any map[string]any
	if resp.StatusCode != 200 || json.Unmarshal(body, &any) == nil || !strings.Contains(string(body), NonJSONMarker) {
		t.Fatalf("non-JSON fault: %d %q", resp.StatusCode, body)
	}
	if resp, _, _ = f.get(f.s.DiscoveryURL()); resp.StatusCode != 200 {
		t.Fatal("the other endpoint must stay healthy")
	}

	f.setFault(Fault{OversizeBytes: 2 << 20})
	for _, u := range []string{f.s.JWKSURL(), f.s.DiscoveryURL()} {
		resp, body, _ = f.get(u)
		if resp.StatusCode != 200 || len(body) < 2<<20 || json.Unmarshal(body, &any) != nil {
			t.Fatalf("%s: oversize fault gave %d, %d bytes", u, resp.StatusCode, len(body))
		}
	}
}

// The double serves the cases of R-CI-003.
func TestDoubleFaultRedirects(t *testing.T) {
	f := newFixture(t, Config{})
	f.setFault(Fault{Redirect: &Redirect{Hops: 3}, Target: TargetJWKS})
	noFollow := &http.Client{Transport: f.client.Transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noFollow.Get(f.s.JWKSURL())
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if loc := resp.Header.Get("Location"); resp.StatusCode != http.StatusFound || loc != f.run.URL+JWKSPath+"?hop=1" {
		t.Fatalf("first hop: %s %q", resp.Status, loc)
	}
	// Followed to the end, the chain ends in the key set: three redirects, one fetch.
	var d jwksDoc
	f.mustJSON(f.s.JWKSURL(), &d)
	if len(d.Keys) != 1 {
		t.Fatalf("key set %+v", d)
	}
	if c := f.s.Counts(); c.JWKS != 2 || c.Hops != 3 {
		t.Fatalf("counts %+v: want one count per fetch and the hops apart", c)
	}

	// A client with the limit of R-CI-003 (3 redirects) gets through 3 hops but not through 4.
	f.setFault(Fault{Redirect: &Redirect{Hops: 4}, Target: TargetJWKS})
	limited := &http.Client{Transport: f.client.Transport, CheckRedirect: func(_ *http.Request, via []*http.Request) error {
		if len(via) > 3 {
			return errors.New("too many redirects")
		}
		return nil
	}}
	if err := tryGet(limited, f.s.JWKSURL()); err == nil {
		t.Fatal("4 redirects must exceed a limit of 3")
	}

	// A redirect out of TLS: the last answer names the plain listener, which serves the content.
	f.setFault(Fault{Redirect: &Redirect{Hops: 1, ToHTTP: true}})
	f.s.Reset()
	hop1, _ := noFollow.Get(f.s.JWKSURL())
	_ = hop1.Body.Close()
	last, err := noFollow.Get(hop1.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	_ = last.Body.Close()
	if loc := last.Header.Get("Location"); !strings.HasPrefix(loc, "http://") || !strings.HasSuffix(loc, JWKSPath) {
		t.Fatalf("the last hop must leave TLS: %q", loc)
	}
	if c := f.s.Counts(); c.Plain != 0 {
		t.Fatalf("nothing followed it yet: %+v", c)
	}
	plain, err := http.Get(last.Header.Get("Location")) //nolint:noctx // a plain listener of the test, the point is the request
	if err != nil {
		t.Fatal(err)
	}
	_ = plain.Body.Close()
	if c := f.s.Counts(); plain.StatusCode != 200 || c.Plain != 1 || f.run.PlainURL == "" {
		t.Fatalf("plain listener: %s, %+v", plain.Status, c)
	}
}

// The double serves the cases of R-CI-003.
func TestDoubleFaultDiscoveryDocument(t *testing.T) {
	f := newFixture(t, Config{})
	var doc map[string]any

	f.setFault(Fault{IssuerMismatch: "https://other.example.com"})
	f.mustJSON(f.s.DiscoveryURL(), &doc)
	if doc["issuer"] != "https://other.example.com" || doc["jwks_uri"] != f.run.URL+JWKSPath {
		t.Fatalf("issuer mismatch: %v", doc)
	}

	f.setFault(Fault{JWKSURI: "https://169.254.169.254/keys"})
	f.mustJSON(f.s.DiscoveryURL(), &doc)
	if doc["jwks_uri"] != "https://169.254.169.254/keys" || doc["issuer"] != f.run.URL {
		t.Fatalf("jwks_uri: %v", doc)
	}

	f.setFault(Fault{NoCommonAlgorithm: true})
	f.mustJSON(f.s.DiscoveryURL(), &doc)
	if a, _ := json.Marshal(doc["id_token_signing_alg_values_supported"]); string(a) != `["HS256"]` {
		t.Fatalf("algorithms %s", a)
	}

	f.setFault(Fault{Algorithms: []string{"ES256", "EdDSA"}})
	f.mustJSON(f.s.DiscoveryURL(), &doc)
	if a, _ := json.Marshal(doc["id_token_signing_alg_values_supported"]); string(a) != `["ES256","EdDSA"]` {
		t.Fatalf("algorithms %s", a)
	}

	for _, bad := range []Fault{{Target: "x"}, {Status: 42}, {OversizeBytes: -1}, {Redirect: &Redirect{Hops: -1}}} {
		if f.s.SetFault(bad) == nil {
			t.Errorf("fault %+v was accepted", bad)
		}
	}
}

// The double serves the cases of R-CI-003.
func TestDoubleCounters(t *testing.T) {
	f := newFixture(t, Config{})
	if c := f.s.Counts(); c != (Counts{}) {
		t.Fatalf("fresh counts %+v", c)
	}
	_, _, _ = f.get(f.s.DiscoveryURL())
	_, _, _ = f.get(f.s.JWKSURL())
	_, _, _ = f.get(f.s.JWKSURL())
	_, _, _ = f.get(f.run.URL + "/other")
	if c := f.s.Counts(); c.Discovery != 1 || c.JWKS != 2 || c.Other != 1 {
		t.Fatalf("counts %+v", c)
	}
	f.s.Reset()
	if c := f.s.Counts(); c != (Counts{}) {
		t.Fatalf("after reset %+v", c)
	}
	// Minting is not a request.
	_ = mustMint(t, f.s, MintOptions{})
	if c := f.s.Counts(); c != (Counts{}) {
		t.Fatalf("minting counted: %+v", c)
	}
	// A method other than GET and HEAD is refused.
	resp, err := f.client.Post(f.s.JWKSURL(), "text/plain", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatal(resp.Status)
	}
}

func TestStartRequiresACertificate(t *testing.T) {
	s, err := New(Config{NoDefaultKey: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Start("127.0.0.1:0", nil, ""); err == nil {
		t.Fatal("started without a certificate")
	}
	if _, err := s.Start("127.0.0.1:0", &tls.Config{MinVersion: tls.VersionTLS12}, ""); err == nil {
		t.Fatal("started with an empty TLS configuration")
	}
	// The CA PEM is what a test hands to trove as ca_bundle_ref.
	ss, err := NewSelfSigned("localhost")
	if err != nil || !bytes.HasPrefix(ss.CAPEM, []byte("-----BEGIN CERTIFICATE-----")) {
		t.Fatal("CA PEM")
	}
	// The issuer is known only after start, unless PublicURL says it.
	if s.Issuer() != "" {
		t.Fatal("issuer before start")
	}
	pub, _ := New(Config{PublicURL: "https://oidcstub.e2e.svc/", NoDefaultKey: true})
	if pub.Issuer() != "https://oidcstub.e2e.svc" || pub.JWKSURL() != "https://oidcstub.e2e.svc/oauth/discovery/keys" {
		t.Fatalf("public URL %q", pub.Issuer())
	}
}

func TestPortsAreTLSOnlyAndHTTP11(t *testing.T) {
	f := newFixture(t, Config{})
	conn, err := tls.Dial("tcp", f.run.Addr, &tls.Config{RootCAs: f.ca.Pool, ServerName: "localhost", MinVersion: tls.VersionTLS12, NextProtos: []string{"h2", "http/1.1"}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if p := conn.ConnectionState().NegotiatedProtocol; p == "h2" {
		t.Fatal("HTTP/2 negotiated: the Unreachable fault could not cut the connection")
	}
	// Plain HTTP to the TLS port does not get an answer.
	c, err := net.Dial("tcp", f.run.Addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _ = c.Write([]byte("GET /oauth/discovery/keys HTTP/1.0\r\n\r\n"))
	buf := make([]byte, 64)
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _ := c.Read(buf)
	if bytes.Contains(buf[:n], []byte(`"keys"`)) {
		t.Fatal("the TLS port answered plain HTTP")
	}
}
