package subject_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/ci/reason"
	"github.com/guided-traffic/trove/internal/ci/subject"
	"github.com/guided-traffic/trove/test/oidcstub"
)

// FuzzR_CI_004_SubjectToken runs the whole validation on arbitrary input (R-CI-004): Parse and
// Verify never panic, every refusal is a *subject.Failure with a reason of R-CI-008, Parse refuses
// only with the structural reasons of steps 1 to 4, and nothing that is not signed by the double's
// key is ever accepted.
//
//	go test ./internal/ci/subject -run '^$' -fuzz '^FuzzR_CI_004_SubjectToken$' -fuzztime 1m
func FuzzR_CI_004_SubjectToken(f *testing.F) {
	stub, err := oidcstub.New(oidcstub.Config{PublicURL: issuerURL, Now: func() time.Time { return now }})
	if err != nil {
		f.Fatal(err)
	}
	for _, o := range []oidcstub.MintOptions{
		{},
		{Alg: "HS256"},
		{Alg: "none"},
		{JWE: true},
		{NoKID: true},
		{Duplicate: map[string]any{"iss": "x"}},
		{InvalidUTF8: true},
		{Headers: map[string]any{"crit": []string{"exp"}}},
		{ForeignSignature: true},
	} {
		tok, err := stub.Mint(o)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(tok)
	}
	f.Add("glcbt-x")
	keys := &source{}
	f.Fuzz(func(t *testing.T, raw string) {
		u, err := subject.Parse(raw, 16384)
		if err != nil {
			var fl *subject.Failure
			if !errors.As(err, &fl) || (fl.Reason != reason.SubjectTokenMalformed && fl.Reason != reason.WrongSubjectTokenType) {
				t.Fatalf("Parse error %v", err)
			}
			return
		}
		if keys.keys == nil {
			keys = keysOf(t, stub)
		}
		v, err := u.Verify(context.Background(), settings(), keys, now)
		if err == nil {
			// Only a token signed with the double's key verifies; the fuzzer mutates signed seeds,
			// so an acceptance must still carry the issuer and the claims it was minted with.
			if iss, _ := v.Claims.First("iss"); iss != issuerURL {
				t.Fatalf("accepted a token of iss %q", iss)
			}
			return
		}
		var fl *subject.Failure
		if !errors.As(err, &fl) {
			t.Fatalf("Verify error %v is not a *subject.Failure", err)
		}
		if status, _ := fl.Reason.OAuth(); status != 400 {
			t.Fatalf("Verify reason %s is not a 400", fl.Reason)
		}
	})
}
