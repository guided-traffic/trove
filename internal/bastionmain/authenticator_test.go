package bastionmain

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/errs"
)

type fakeValidator struct {
	info  token.BastionInfo
	err   error
	calls int
	noted []string
}

func (f *fakeValidator) AuthenticateBastion(_ context.Context, _ string, _ token.Request) (token.BastionInfo, error) {
	f.calls++
	return f.info, f.err
}

func (f *fakeValidator) NoteUse(id, addr string) { f.noted = append(f.noted, id+"@"+addr) }

// TestR_BAS_004_TheAuthenticatorMapsEveryVerdictToOneError: a malformed, unknown, revoked or expired token and a
// disabled registration are one error, ErrTokenInvalid, whichever it is (R-AUTH-002); a store that does not
// answer is the store's error, and none of them is a use of the token.
func TestR_BAS_004_TheAuthenticatorMapsEveryVerdictToOneError(t *testing.T) {
	for _, verdict := range []error{token.ErrMalformed, token.ErrUnknown, token.ErrRevoked, token.ErrExpired, token.ErrOwner} {
		v := &fakeValidator{err: verdict}
		_, err := NewAuthenticator(v, nil).Authenticate(context.Background(), "x")
		if !errors.Is(err, ErrTokenInvalid) {
			t.Errorf("%v: err = %v, want ErrTokenInvalid", verdict, err)
		}
		if len(v.noted) != 0 {
			t.Errorf("%v: a refused token was noted as used", verdict)
		}
	}
	down := errs.E(errs.Unavailable, errs.CodeDatabaseUnavailable, "down")
	_, err := NewAuthenticator(&fakeValidator{err: down}, nil).Authenticate(context.Background(), "x")
	if errors.Is(err, ErrTokenInvalid) || !errors.Is(err, down) {
		t.Fatalf("a store failure became a verdict on the token: %v", err)
	}
}

// TestR_BAS_004_TheAuthenticatorReturnsTheRegistrationAndNotesTheUse: the answer names the token and the
// registration, and the use is recorded for lastUsedAt with the request's address (R-AUTH-031).
func TestR_BAS_004_TheAuthenticatorReturnsTheRegistrationAndNotesTheUse(t *testing.T) {
	exp := time.Date(2027, 11, 2, 10, 0, 0, 0, time.UTC)
	v := &fakeValidator{info: token.BastionInfo{TokenID: "t1", Hint: "trv_bst_3kQ9", BastionID: "b1", Name: "partners", ExpiresAt: exp}}
	got, err := NewAuthenticator(v, func(context.Context) string { return "198.51.100.20" }).Authenticate(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	want := BastionToken{TokenID: "t1", Hint: "trv_bst_3kQ9", BastionID: "b1", Name: "partners", ExpiresAt: exp}
	if got != want {
		t.Fatalf("token = %+v, want %+v", got, want)
	}
	if len(v.noted) != 1 || v.noted[0] != "t1@198.51.100.20" {
		t.Fatalf("noted = %v", v.noted)
	}
}

func TestR_BAS_004_TheNameRule(t *testing.T) {
	for name, ok := range map[string]bool{
		"partners": true, "a": true, "a-b-9": true, "abcdefghijklmnopqrst": true,
		"": false, "Partners": false, "1abc": false, "-ab": false, "a_b": false, "abcdefghijklmnopqrstu": false, "a b": false,
	} {
		if ValidName(name) != ok {
			t.Errorf("ValidName(%q) = %v, want %v", name, !ok, ok)
		}
	}
}
