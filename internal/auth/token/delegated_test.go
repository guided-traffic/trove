package token

import (
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/errs"
)

// TestR_AUTH_027_DelegatedTokenRules is the rule set of a token a project administrator
// issues (Q-AUTHZ-177, Q-AUTH-192): repository rights only, and never self-rotatable.
func TestR_AUTH_027_DelegatedTokenRules(t *testing.T) {
	cases := []struct {
		name          string
		sc            scope.Scope
		selfRotatable bool
		code, pointer string
	}{
		{"Q-AUTHZ-177 repository rights only are allowed", scope.Scope{Inherit: true}, false, "", ""},
		{"Q-AUTHZ-177 a system item is refused", scope.Scope{Inherit: true, System: []string{"identities:read"}}, false, errs.CodeInvalidScope, ""},
		{"Q-AUTHZ-177 admin true is refused", scope.Scope{Inherit: true, Admin: true}, false, errs.CodeInvalidScope, ""},
		{"Q-AUTH-192 a self-rotatable token is refused", scope.Scope{Inherit: true}, true, errs.CodeValidation, "/selfRotatable"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := checkDelegated(c.sc, c.selfRotatable)
			if c.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			e, ok := errs.As(err)
			if !ok || e.Code != c.code || e.Status() != 422 {
				t.Fatalf("%v", err)
			}
			if c.pointer != "" && (len(e.Fields) != 1 || e.Fields[0].Pointer != c.pointer) {
				t.Fatalf("fields %+v", e.Fields)
			}
		})
	}
}
