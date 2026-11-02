package v1

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
)

func ciRequest(kind httpserver.PrincipalKind) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/x", nil)
	p := httpserver.Principal{Kind: kind, ID: "0192f0c4-7a7e-7b51-9b1e-2f0d6c9e1a10", Name: "gitlab/release"}
	return r.WithContext(httpserver.WithPrincipal(r.Context(), p))
}

// TestR_CI_013_ACITokenIsRefusedOnTheManagementAPIByDefault: the gate admits a CI token only on the
// operations of the action form, on public operations and on those that declare x-trove-ci: admitted
// (Q-CI-18); every other form is refused with 403 ci_token_restricted, and every other caller passes.
func TestR_CI_013_ACITokenIsRefusedOnTheManagementAPIByDefault(t *testing.T) {
	cases := []struct {
		name string
		op   Operation
		kind httpserver.PrincipalKind
		want bool // refused
	}{
		{"a capability", Operation{Requirement: Requirement{Permission: "ci:read"}}, httpserver.KindCI, true},
		{"a role", Operation{Requirement: Requirement{Role: "admin"}}, httpserver.KindCI, true},
		{"authenticated", Operation{Requirement: Requirement{Authenticated: true}}, httpserver.KindCI, true},
		{"authenticated and admitted", Operation{Requirement: Requirement{Authenticated: true}, CIAdmitted: true}, httpserver.KindCI, false},
		{"a repository action", Operation{Requirement: Requirement{Action: "read", Repository: "path.repoKey"}}, httpserver.KindCI, false},
		{"public", Operation{Requirement: Requirement{Public: true}}, httpserver.KindCI, false},
		{"a user on a capability", Operation{Requirement: Requirement{Permission: "ci:read"}}, httpserver.KindUser, false},
	}
	for _, tc := range cases {
		t.Run("R-CI-013 "+tc.name, func(t *testing.T) {
			err := ciGate(ciRequest(tc.kind), tc.op)
			if (err != nil) != tc.want {
				t.Fatalf("refused = %v, want %v (%v)", err != nil, tc.want, err)
			}
			if err != nil {
				if e, ok := errs.As(err); !ok || e.Kind != errs.Forbidden || e.Code != errs.CodeCITokenRestricted {
					t.Fatalf("error = %#v", err)
				}
			}
		})
	}
}

// TestR_CI_017_OnlyTheCurrentTokenOperationsAdmitACIToken: of the document's operations, exactly
// GET and DELETE /tokens/current declare x-trove-ci: admitted (R-CI-013, R-CI-017).
func TestR_CI_017_OnlyTheCurrentTokenOperationsAdmitACIToken(t *testing.T) {
	doc, err := LoadDocument(EmbeddedYAML())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, op := range doc.Operations() {
		if op.CIAdmitted {
			got = append(got, op.ID)
		}
	}
	if len(got) != 2 {
		t.Fatalf("admitted: %v", got)
	}
	for _, id := range got {
		if id != "getCurrentToken" && id != "revokeCurrentToken" {
			t.Errorf("%s admits a CI token", id)
		}
	}
}
