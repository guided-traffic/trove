package visibility

import (
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
)

// TestR_AUTHZ_033_TheOrderOfTheLevels: private comes before internal before public, and a string that is no level is none of them.
func TestR_AUTHZ_033_TheOrderOfTheLevels(t *testing.T) {
	order := []string{Private, Internal, Public}
	for i, a := range order {
		if !Valid(a) || Rank(a) != i {
			t.Errorf("%s: valid %v rank %d, want %d", a, Valid(a), Rank(a), i)
		}
		for j, b := range order {
			if got := Above(a, b); got != (i > j) {
				t.Errorf("Above(%s, %s) = %v", a, b, got)
			}
			want := a
			if j < i {
				want = b
			}
			if got := Lower(a, b); got != want {
				t.Errorf("Lower(%s, %s) = %s, want %s", a, b, got, want)
			}
		}
	}
	for _, s := range []string{"", "Private", "open", "authenticated", "anonymous"} {
		if Valid(s) || Rank(s) != -1 {
			t.Errorf("%q is taken for a level", s)
		}
	}
}

// TestR_AUTHZ_033_PublicNotAllowedIsA409: the refusal of public while the default endpoint admits no request without a credential.
func TestR_AUTHZ_033_PublicNotAllowedIsA409(t *testing.T) {
	e, ok := errs.As(PublicNotAllowed())
	if !ok || e.Status() != 409 || e.Code != errs.CodePublicNotAllowed {
		t.Fatalf("%v", PublicNotAllowed())
	}
	if f := InvalidLevel("/visibility/wiki"); f.Pointer != "/visibility/wiki" || f.Code != "invalid_value" {
		t.Errorf("%+v", f)
	}
}
