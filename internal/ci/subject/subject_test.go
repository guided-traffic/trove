package subject_test

import (
	"context"
	"crypto/rsa"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/jws"
	"github.com/guided-traffic/trove/internal/ci/reason"
	"github.com/guided-traffic/trove/internal/ci/subject"
	"github.com/guided-traffic/trove/test/oidcstub"
)

const issuerURL = "https://gitlab.example.com"

// now is the clock of the double and of every Verify in these tests.
var now = time.Unix(1792936400, 0)

func newStub(t *testing.T) *oidcstub.Server {
	t.Helper()
	s, err := oidcstub.New(oidcstub.Config{PublicURL: issuerURL, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// settings are the defaults of a gitlab issuer at https://packages.example.com (R-CI-001, R-CI-006).
func settings() subject.Settings {
	return subject.Settings{
		Issuer:            issuerURL,
		AllowedAudiences:  []string{"https://packages.example.com", "npm:packages.example.com"},
		ExclusiveAudience: true,
		AllowedAlgorithms: []string{"RS256"},
		AllowedTyp:        []string{"JWT"},
		AllowMissingTyp:   true,
		ClockSkew:         60 * time.Second,
		RequiredClaims: []string{
			"iss", "aud", "exp", "iat", "jti", "sub", "project_id", "namespace_id",
			"project_path", "namespace_path", "ref", "ref_type", "ref_protected", "pipeline_source",
			"pipeline_id", "job_id", "user_id", "user_login",
		},
	}
}

// source is a key source over the double's published key set, as the key store builds it from the
// stored JWKS (W1). It counts its calls and can answer an error.
type source struct {
	keys  []subject.Key
	err   error
	calls int
	kids  []string
}

func (s *source) Keys(_ context.Context, kid, alg string) ([]subject.Key, error) {
	s.calls++
	s.kids = append(s.kids, kid)
	if s.err != nil {
		return nil, s.err
	}
	var out []subject.Key
	for _, k := range s.keys {
		if kid != "" && k.KID != kid {
			continue
		}
		if kty, _ := jws.KeyType(alg); kid == "" && k.KTY != kty {
			continue
		}
		out = append(out, k)
	}
	if len(out) == 0 {
		return nil, subject.ErrUnknownKey
	}
	return out, nil
}

func keysOf(t *testing.T, stub *oidcstub.Server) *source {
	t.Helper()
	rec := httptest.NewRecorder()
	stub.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, oidcstub.JWKSPath, nil))
	set, err := jws.ParseJWKS(rec.Body.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	s := &source{}
	for _, k := range set.Keys {
		s.keys = append(s.keys, subject.Key{KID: k.KID, KTY: k.KTY, Alg: k.Alg, Public: k.Public})
	}
	return s
}

func mint(t *testing.T, stub *oidcstub.Server, o oidcstub.MintOptions) string {
	t.Helper()
	tok, err := stub.Mint(o)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// validate is the caller's sequence: Parse, the issuer lookup by iss, Verify.
func validate(t *testing.T, raw string, s subject.Settings, keys subject.KeySource) (subject.Verified, error) {
	t.Helper()
	u, err := subject.Parse(raw, 16384)
	if err != nil {
		return subject.Verified{}, err
	}
	if err := subject.CheckIssuer(u.Issuer == s.Issuer, true); err != nil {
		return subject.Verified{}, err
	}
	return u.Verify(context.Background(), s, keys, now)
}

func reasonOf(t *testing.T, err error) reason.Reason {
	t.Helper()
	var f *subject.Failure
	if !errors.As(err, &f) {
		t.Fatalf("error %v is not a *subject.Failure", err)
	}
	return f.Reason
}

func expectReason(t *testing.T, err error, want reason.Reason, raw string) {
	t.Helper()
	if err == nil {
		t.Fatalf("accepted, want %s", want)
	}
	if got := reasonOf(t, err); got != want {
		t.Fatalf("reason %s (%v), want %s", got, err, want)
	}
	// R-CI-008: no fragment of the token in the error.
	for _, part := range strings.Split(raw, ".") {
		if len(part) > 8 && strings.Contains(err.Error(), part[:8]) {
			t.Fatalf("the error holds token material: %v", err)
		}
	}
}

// The double's default token lives one hour, as GitLab 19.0.1's tokens of a job with the default timeout did.
func TestR_CI_004_AGitLabIDTokenIsAccepted(t *testing.T) {
	stub := newStub(t)
	keys := keysOf(t, stub)
	raw := mint(t, stub, oidcstub.MintOptions{})
	v, err := validate(t, raw, settings(), keys)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := v.Claims.First("project_id"); got != "1234" {
		t.Fatalf("project_id %q", got)
	}
	if v.JTI == "" || v.ExpiresAt != now.Add(time.Hour) || v.IssuedAt != now || v.NotBefore != now.Add(-5*time.Second) {
		t.Fatalf("%+v", v)
	}
	if v.Header.Alg != "RS256" || v.Header.KID == "" || keys.kids[0] != v.Header.KID {
		t.Fatalf("header %+v, kids %v", v.Header, keys.kids)
	}
}

func TestR_CI_004_EachStepRefusesWithItsReason(t *testing.T) {
	stub := newStub(t)
	add := func(o oidcstub.KeyOptions) *oidcstub.Key {
		k, err := stub.AddKey(o)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	add(oidcstub.KeyOptions{Type: oidcstub.ES256})
	small := add(oidcstub.KeyOptions{Type: oidcstub.RSA1024}) // published, and ignored by the key rules
	wrongAlg := add(oidcstub.KeyOptions{Alg: "RS384"})
	unpublished := add(oidcstub.KeyOptions{Unpublished: true})
	add(oidcstub.KeyOptions{}) // Mint signs RS256 with the newest published RSA-2048 key
	keys := keysOf(t, stub)
	s := settings()

	cases := []struct {
		name string
		opts oidcstub.MintOptions
		raw  string
		set  func(*subject.Settings)
		want reason.Reason
	}{
		// Step 1
		{name: "1 a token over max_subject_token_bytes", opts: oidcstub.MintOptions{PadTo: 16385}, want: reason.SubjectTokenMalformed},
		// Step 2
		{name: "2 a GitLab job token", raw: "glcbt-64_abcdefghijklmnopqrstu", want: reason.WrongSubjectTokenType},
		{name: "2 a JWE", opts: oidcstub.MintOptions{JWE: true}, want: reason.SubjectTokenMalformed},
		{name: "2 an unsecured JWT", opts: oidcstub.MintOptions{Alg: "none"}, want: reason.SubjectTokenMalformed},
		{name: "2 a nested JWT", opts: oidcstub.MintOptions{Headers: map[string]any{"cty": "JWT"}}, want: reason.SubjectTokenMalformed},
		{name: "2 an opaque string", raw: "not-a-jwt", want: reason.SubjectTokenMalformed},
		// Step 3
		{name: "3 the algorithm-confusion token (HS256, the PEM of the RSA key, Q-CI-24)", opts: oidcstub.MintOptions{Alg: "HS256"}, want: reason.AlgorithmNotAllowed},
		{name: "3 HS256 even when the allow list names it", opts: oidcstub.MintOptions{Alg: "HS256"}, set: func(s *subject.Settings) { s.AllowedAlgorithms = append(s.AllowedAlgorithms, "HS256") }, want: reason.AlgorithmNotAllowed},
		{name: "3 PS256 outside allowed_algorithms", opts: oidcstub.MintOptions{Alg: "PS256"}, want: reason.AlgorithmNotAllowed},
		{name: "3 ES256 outside allowed_algorithms", opts: oidcstub.MintOptions{Alg: "ES256"}, want: reason.AlgorithmNotAllowed},
		{name: "3 a typ outside allowed_typ", opts: oidcstub.MintOptions{Typ: "at+jwt"}, want: reason.TokenTypeNotAllowed},
		{name: "3 no typ without allow_missing_typ", opts: oidcstub.MintOptions{NoTyp: true}, set: func(s *subject.Settings) { s.AllowMissingTyp = false }, want: reason.TokenTypeNotAllowed},
		{name: "3 a crit header", opts: oidcstub.MintOptions{Headers: map[string]any{"crit": []string{"exp"}}}, want: reason.SubjectTokenMalformed},
		{name: "3 a kid of 300 characters", opts: oidcstub.MintOptions{HeaderKID: strings.Repeat("k", 300)}, want: reason.SubjectTokenMalformed},
		// Step 4
		{name: "4 a duplicate claim name", opts: oidcstub.MintOptions{Duplicate: map[string]any{"project_id": "900"}}, want: reason.SubjectTokenMalformed},
		{name: "4 a payload that is not UTF-8", opts: oidcstub.MintOptions{InvalidUTF8: true}, want: reason.SubjectTokenMalformed},
		{name: "4 a payload that is an array", opts: oidcstub.MintOptions{RawPayload: []byte(`["iss"]`)}, want: reason.SubjectTokenMalformed},
		// Step 5
		{name: "5 an unknown issuer", opts: oidcstub.MintOptions{Claims: map[string]any{"iss": "https://gitlab.com"}}, want: reason.UnknownIssuer},
		// Step 6
		{name: "6 an unknown kid", opts: oidcstub.MintOptions{HeaderKID: "rotated-away"}, want: reason.UnknownSigningKey},
		{name: "6 a signature by a foreign key under a published kid", opts: oidcstub.MintOptions{ForeignSignature: true}, want: reason.SignatureInvalid},
		{name: "6 a key that is not published", opts: oidcstub.MintOptions{KID: unpublished.KID}, want: reason.UnknownSigningKey},
		{name: "6 a 1024-bit RSA key", opts: oidcstub.MintOptions{KID: small.KID}, want: reason.UnknownSigningKey},
		{name: "6 a key whose own alg is not the token's", opts: oidcstub.MintOptions{KID: wrongAlg.KID}, want: reason.UnknownSigningKey},
		{name: "6 a header jwk of the signer is ignored", opts: oidcstub.MintOptions{ForeignSignature: true, Headers: map[string]any{"jku": "https://attacker.example/keys", "x5u": "https://attacker.example/c"}}, want: reason.SignatureInvalid},
		// Step 7
		{name: "7 an audience of another service", opts: oidcstub.MintOptions{Claims: map[string]any{"aud": "https://other.example.com"}}, want: reason.AudienceInvalid},
		{name: "7 an audience list that also names vault", opts: oidcstub.MintOptions{Claims: map[string]any{"aud": []string{"https://packages.example.com", "https://vault.example.com"}}}, want: reason.AudienceNotExclusive},
		{name: "7 no audience", opts: oidcstub.MintOptions{Remove: []string{"aud"}}, want: reason.AudienceInvalid},
		{name: "7 an audience that is a number", opts: oidcstub.MintOptions{Claims: map[string]any{"aud": 7}}, want: reason.AudienceInvalid},
		{name: "7 an empty audience list", opts: oidcstub.MintOptions{Claims: map[string]any{"aud": []string{}}}, want: reason.AudienceInvalid},
		// Step 8
		{name: "8 exp 61 seconds ago with a 60-second skew", opts: oidcstub.MintOptions{Claims: map[string]any{"exp": now.Unix() - 61}}, want: reason.TokenExpired},
		{name: "8 nbf 61 seconds ahead", opts: oidcstub.MintOptions{Claims: map[string]any{"nbf": now.Unix() + 61}}, want: reason.TokenNotYetValid},
		{name: "8 iat 61 seconds ahead", opts: oidcstub.MintOptions{Claims: map[string]any{"iat": now.Unix() + 61}}, want: reason.IssuedInFuture},
		{name: "8 no exp (Q-CI-27)", opts: oidcstub.MintOptions{Remove: []string{"exp"}}, want: reason.RequiredClaimMissing},
		{name: "8 a null iat (Q-CI-27)", opts: oidcstub.MintOptions{Claims: map[string]any{"iat": nil}}, want: reason.RequiredClaimMissing},
		{name: "8 an exp that is a string (Q-CI-27)", opts: oidcstub.MintOptions{Claims: map[string]any{"exp": "1792940000"}}, want: reason.SubjectTokenMalformed},
		{name: "8 a negative nbf (Q-CI-27)", opts: oidcstub.MintOptions{Claims: map[string]any{"nbf": -1}}, want: reason.SubjectTokenMalformed},
		// Step 9
		{name: "9 no project_id", opts: oidcstub.MintOptions{Remove: []string{"project_id"}}, want: reason.RequiredClaimMissing},
		{name: "9 a null jti", opts: oidcstub.MintOptions{Claims: map[string]any{"jti": nil}}, want: reason.RequiredClaimMissing},
	}
	for _, c := range cases {
		t.Run("R-CI-004 step "+c.name+" is "+string(c.want), func(t *testing.T) {
			raw := c.raw
			if raw == "" {
				raw = mint(t, stub, c.opts)
			}
			st := s
			st.AllowedAudiences = append([]string(nil), s.AllowedAudiences...)
			st.AllowedAlgorithms = append([]string(nil), s.AllowedAlgorithms...)
			if c.set != nil {
				c.set(&st)
			}
			_, err := validate(t, raw, st, keys)
			expectReason(t, err, c.want, raw)
		})
	}
	t.Run("R-CI-004 step 9 names the missing claim", func(t *testing.T) {
		_, err := validate(t, mint(t, stub, oidcstub.MintOptions{Remove: []string{"namespace_id"}}), s, keys)
		var f *subject.Failure
		if !errors.As(err, &f) || !strings.Contains(f.Detail, "namespace_id") {
			t.Fatalf("%v", err)
		}
	})
}

func TestR_CI_004_TheStepsRunInOrder(t *testing.T) {
	stub := newStub(t)
	keys := keysOf(t, stub)
	s := settings()
	cases := []struct {
		name string
		opts oidcstub.MintOptions
		want reason.Reason
	}{
		{"a bad signature wins over a wrong audience and an expired token", oidcstub.MintOptions{ForeignSignature: true, Claims: map[string]any{"aud": "x", "exp": 1}}, reason.SignatureInvalid},
		{"a refused algorithm wins over a bad signature", oidcstub.MintOptions{Alg: "PS256", ForeignSignature: true}, reason.AlgorithmNotAllowed},
		{"an unknown issuer wins over a refused algorithm (Q-CI-19)", oidcstub.MintOptions{Alg: "HS256", Claims: map[string]any{"iss": "https://gitlab.com"}}, reason.UnknownIssuer},
		{"a wrong audience wins over an expired token", oidcstub.MintOptions{Claims: map[string]any{"aud": "x", "exp": 1}}, reason.AudienceInvalid},
		{"an expired token wins over a missing required claim", oidcstub.MintOptions{Claims: map[string]any{"exp": 1}, Remove: []string{"project_id"}}, reason.TokenExpired},
		{"a malformed payload wins over everything after it", oidcstub.MintOptions{Alg: "HS256", Duplicate: map[string]any{"iss": issuerURL}}, reason.SubjectTokenMalformed},
	}
	for _, c := range cases {
		t.Run("R-CI-004 "+c.name, func(t *testing.T) {
			raw := mint(t, stub, c.opts)
			_, err := validate(t, raw, s, keys)
			expectReason(t, err, c.want, raw)
		})
	}
	t.Run("R-CI-004 a disabled issuer answers issuer_disabled before the token's algorithm is judged", func(t *testing.T) {
		u, err := subject.Parse(mint(t, stub, oidcstub.MintOptions{Alg: "HS256"}), 16384)
		if err != nil {
			t.Fatal(err)
		}
		if got := reasonOf(t, subject.CheckIssuer(u.Issuer == issuerURL, false)); got != reason.IssuerDisabled {
			t.Fatalf("%s", got)
		}
	})
	t.Run("R-CI-004 nothing but iss is read before the signature: the key source sees only kid and alg", func(t *testing.T) {
		src := &source{err: subject.ErrUnknownKey}
		raw := mint(t, stub, oidcstub.MintOptions{Claims: map[string]any{"aud": "x"}})
		_, err := validate(t, raw, s, src)
		expectReason(t, err, reason.UnknownSigningKey, raw)
		if src.calls != 1 {
			t.Fatalf("%d lookups", src.calls)
		}
	})
}

func TestR_CI_004_TheAudienceCases(t *testing.T) {
	stub := newStub(t)
	keys := keysOf(t, stub)
	cases := []struct {
		name    string
		allowed []string
		aud     any
		want    reason.Reason
	}{
		{"two allowed audiences, exactly those two", []string{"https://packages.example.com", "https://packages.internal.example.com"}, []string{"https://packages.example.com", "https://packages.internal.example.com"}, ""},
		{"vault beside trove under the defaults", nil, []string{"https://packages.example.com", "https://vault.example.com"}, reason.AudienceNotExclusive},
		{"the npm audience under the defaults", nil, "npm:packages.example.com", ""},
		{"the base URL and the npm audience under the defaults", nil, []string{"https://packages.example.com", "npm:packages.example.com"}, ""},
		{"trove's npm audience and the public registry's", nil, []string{"npm:packages.example.com", "npm:registry.npmjs.org"}, reason.AudienceNotExclusive},
		{"the public registry's npm audience alone", nil, "npm:registry.npmjs.org", reason.AudienceInvalid},
		{"another service under the defaults", nil, "https://other.example.com", reason.AudienceInvalid},
	}
	for _, c := range cases {
		name := "R-CI-004 " + c.name + " is accepted"
		if c.want != "" {
			name = "R-CI-004 " + c.name + " is " + string(c.want)
		}
		t.Run(name, func(t *testing.T) {
			s := settings()
			if c.allowed != nil {
				s.AllowedAudiences = c.allowed
			}
			raw := mint(t, stub, oidcstub.MintOptions{Claims: map[string]any{"aud": c.aud}})
			_, err := validate(t, raw, s, keys)
			if c.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			expectReason(t, err, c.want, raw)
		})
	}
	t.Run("R-CI-004 without exclusive_audience another recipient beside trove is accepted", func(t *testing.T) {
		s := settings()
		s.ExclusiveAudience = false
		raw := mint(t, stub, oidcstub.MintOptions{Claims: map[string]any{"aud": []string{"https://packages.example.com", "https://vault.example.com"}}})
		if _, err := validate(t, raw, s, keys); err != nil {
			t.Fatal(err)
		}
	})
}

func TestR_CI_004_TheSkewCases(t *testing.T) {
	stub := newStub(t)
	keys := keysOf(t, stub)
	cases := []struct {
		name   string
		claims map[string]any
		ok     bool
	}{
		{"exp 30 seconds ago", map[string]any{"exp": now.Unix() - 30}, true},
		{"exp 61 seconds ago", map[string]any{"exp": now.Unix() - 61}, false},
		{"exp 60 seconds ago (now < exp + skew is false)", map[string]any{"exp": now.Unix() - 60}, false},
		{"nbf 60 seconds ahead", map[string]any{"nbf": now.Unix() + 60}, true},
		{"nbf 61 seconds ahead", map[string]any{"nbf": now.Unix() + 61}, false},
		{"iat 60 seconds ahead", map[string]any{"iat": now.Unix() + 60}, true},
		{"iat 61 seconds ahead", map[string]any{"iat": now.Unix() + 61}, false},
		{"a fractional exp", map[string]any{"exp": float64(now.Unix()) + 0.5}, true},
		{"a fractional exp 60.5 seconds ago", map[string]any{"exp": float64(now.Unix()) - 60.5}, false},
		{"a null nbf counts as absent", map[string]any{"nbf": nil}, true},
	}
	for _, c := range cases {
		t.Run("R-CI-004 with a 60-second skew, "+c.name, func(t *testing.T) {
			raw := mint(t, stub, oidcstub.MintOptions{Claims: c.claims})
			_, err := validate(t, raw, settings(), keys)
			if c.ok != (err == nil) {
				t.Fatalf("ok %v, err %v", c.ok, err)
			}
		})
	}
	t.Run("R-CI-004 a fractional NumericDate keeps its fraction", func(t *testing.T) {
		raw := mint(t, stub, oidcstub.MintOptions{Claims: map[string]any{"exp": float64(now.Unix()) + 100.25}})
		v, err := validate(t, raw, settings(), keys)
		if err != nil {
			t.Fatal(err)
		}
		if want := now.Add(100*time.Second + 250*time.Millisecond); !v.ExpiresAt.Equal(want) {
			t.Fatalf("exp %v, want %v", v.ExpiresAt, want)
		}
	})
}

func TestR_CI_004_SignatureKeys(t *testing.T) {
	stub := newStub(t)
	if _, err := stub.AddKey(oidcstub.KeyOptions{Type: oidcstub.ES256}); err != nil {
		t.Fatal(err)
	}
	if _, err := stub.AddKey(oidcstub.KeyOptions{Type: oidcstub.EdDSA}); err != nil {
		t.Fatal(err)
	}
	keys := keysOf(t, stub)
	all := settings()
	all.AllowedAlgorithms = jws.Algorithms()
	for _, alg := range []string{"RS256", "RS384", "RS512", "PS256", "PS384", "PS512", "ES256", "EdDSA"} {
		t.Run("R-CI-004 a token signed "+alg+" verifies when the issuer allows it", func(t *testing.T) {
			// The double's RSA key publishes alg RS256; the other RSA algorithms need a key without one.
			src := keys
			if strings.HasPrefix(alg, "RS") && alg != "RS256" || strings.HasPrefix(alg, "PS") {
				src = withoutAlg(keys)
			}
			if _, err := validate(t, mint(t, stub, oidcstub.MintOptions{Alg: alg}), all, src); err != nil {
				t.Fatal(err)
			}
		})
	}
	t.Run("R-CI-004 a token without kid tries the keys of its type", func(t *testing.T) {
		if _, err := validate(t, mint(t, stub, oidcstub.MintOptions{NoKID: true}), settings(), keys); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("R-CI-004 a token without kid tries at most 10 keys", func(t *testing.T) {
		src := &source{}
		for range 11 {
			src.keys = append(src.keys, keys.keys[0])
		}
		signer, err := stub.AddKey(oidcstub.KeyOptions{NoKID: true})
		if err != nil {
			t.Fatal(err)
		}
		pub := signer.Public().(*rsa.PublicKey)
		src.keys = append(src.keys, subject.Key{KTY: "RSA", Alg: "RS256", Public: pub})
		raw := mint(t, stub, oidcstub.MintOptions{SignWith: signer, NoKID: true})
		_, err = validate(t, raw, settings(), src)
		expectReason(t, err, reason.SignatureInvalid, raw)
		src.keys = append([]subject.Key{src.keys[len(src.keys)-1]}, src.keys[:10]...)
		if _, err := validate(t, raw, settings(), src); err != nil {
			t.Fatalf("the signing key among the first 10: %v", err)
		}
	})
	t.Run("R-CI-004 an unavailable key set is returned as is, for the 503", func(t *testing.T) {
		_, err := validate(t, mint(t, stub, oidcstub.MintOptions{}), settings(), &source{err: subject.ErrKeysUnavailable})
		if !errors.Is(err, subject.ErrKeysUnavailable) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("R-CI-004 a key source that answers a key of another kid is not trusted for it", func(t *testing.T) {
		liar := &source{keys: []subject.Key{{KID: "other", KTY: "RSA", Public: keys.keys[0].Public}}}
		raw := mint(t, stub, oidcstub.MintOptions{HeaderKID: "other-kid"})
		u, err := subject.Parse(raw, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, err = u.Verify(context.Background(), settings(), lying{liar}, now)
		expectReason(t, err, reason.UnknownSigningKey, raw)
	})
	t.Run("R-CI-004 a zero Unverified does not verify", func(t *testing.T) {
		_, err := subject.Unverified{Issuer: issuerURL}.Verify(context.Background(), settings(), keys, now)
		if reasonOf(t, err) != reason.SubjectTokenMalformed {
			t.Fatal(err)
		}
	})
}

// lying answers its keys whatever kid is asked.
type lying struct{ s *source }

func (l lying) Keys(context.Context, string, string) ([]subject.Key, error) { return l.s.keys, nil }

func withoutAlg(s *source) *source {
	out := &source{}
	for _, k := range s.keys {
		k.Alg = ""
		out.keys = append(out.keys, k)
	}
	return out
}

func TestAJobTokenNeverPassesAsAnIDToken(t *testing.T) {
	// R-CI-006: the legacy format is wrong_subject_token_type; a JWT-shaped job token of another
	// issuer is unknown_issuer; neither panics nor yields claims.
	for raw, want := range map[string]reason.Reason{
		"glcbt-64_abcdef":                       reason.WrongSubjectTokenType,
		"glcbt-":                                reason.WrongSubjectTokenType,
		"eyJhbGciOiJSUzI1NiJ9.eyJpc3MiOiJ4In0.": reason.SubjectTokenMalformed,
	} {
		_, err := validate(t, raw, settings(), &source{})
		expectReason(t, err, want, raw)
	}
}
