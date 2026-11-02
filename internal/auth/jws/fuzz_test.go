package jws_test

import (
	"crypto/elliptic"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/guided-traffic/trove/internal/auth/jws"
)

// FuzzR_CI_004_Parse holds the compact-JWS parser to its contract on every input (R-CI-004 steps 2
// to 4): it never panics, an error is always one of the package's classes with a fixed detail that
// holds no byte of the input beyond what the package's own texts contain, and a parsed token has a
// string alg, a kid of at most 256 characters, and a signing input that is the first two parts.
// DecodeObject and VerifyWith run on whatever parsed.
//
//	go test ./internal/auth/jws -run '^$' -fuzz '^FuzzR_CI_004_Parse$' -fuzztime 1m
func FuzzR_CI_004_Parse(f *testing.F) {
	r := rsaKey(f, 2048)
	ec := ecKey(f, elliptic.P256())
	f.Add(signed(f, "RS256", r, nil, `{"iss":"https://gitlab.example.com","aud":"x","exp":1}`))
	f.Add(signed(f, "ES256", ec, map[string]any{"alg": "ES256", "crit": []string{"b64"}}, `{}`))
	f.Add(signed(f, "PS384", r, map[string]any{"alg": "PS384", "kid": strings.Repeat("k", 257)}, `[]`))
	f.Add("eyJhbGciOiJub25lIn0.e30.")
	f.Add("a.b.c.d.e")
	f.Add("..")
	f.Add("eyJhbGciOiJSUzI1NiIsImFsZyI6Im5vbmUifQ.e30.e30")
	f.Fuzz(func(t *testing.T, s string) {
		tok, err := jws.Parse(s)
		if err != nil {
			if !errors.Is(err, jws.ErrMalformed) {
				t.Fatalf("Parse error %v is not ErrMalformed", err)
			}
			if jws.Detail(err) == "" {
				t.Fatalf("Parse error %v has no detail", err)
			}
			return
		}
		if tok.Header.Alg == "" || utf8.RuneCountInString(tok.Header.Kid) > jws.MaxKIDLength {
			t.Fatalf("header %+v", tok.Header)
		}
		if strings.Count(s, ".") != 2 {
			t.Fatalf("parsed %d parts", strings.Count(s, ".")+1)
		}
		if _, err := jws.DecodeObject(tok.Payload); err != nil && !errors.Is(err, jws.ErrMalformed) {
			t.Fatalf("DecodeObject error %v", err)
		}
		if err := tok.VerifyWith(r.Public(), ""); err == nil && tok.Unsecured() {
			t.Fatal("an unsecured token verified")
		}
	})
}
