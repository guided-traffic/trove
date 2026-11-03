package authz

import (
	"context"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/errs"
)

// TestR_BAS_004_ABastionReferenceParsesSoThatEveryWriteRefusesIt: bastion:<name> is a reference of the kind
// bastion, so a policy entry, a role binding and an owner record refuse it with 422 principal_not_allowed
// instead of "unknown principal"; the refusals of the policy entry and the role binding are shown over HTTP in
// test/integration/bastionreg.
func TestR_BAS_004_ABastionReferenceParsesSoThatEveryWriteRefusesIt(t *testing.T) {
	r, err := ParseRef("bastion:partners")
	if err != nil || r.Kind != principal.KindBastion || r.Name != "partners" || r.String() != "bastion:partners" {
		t.Fatalf("ParseRef = %+v, %v", r, err)
	}
	if got := refOfRow("bastion", "partners"); got != "bastion:partners" {
		t.Fatalf("refOfRow = %q", got)
	}
}

// TestR_BAS_004_AnOwnerRecordRefusesABastion: the owner operations answer 422 principal_not_allowed before any
// lookup.
func TestR_BAS_004_AnOwnerRecordRefusesABastion(t *testing.T) {
	_, _, err := ownerRef(context.Background(), nil, "bastion:partners")
	e, ok := errs.As(err)
	if !ok || e.Status() != 422 || e.Code != errs.CodePrincipalNotAllowed {
		t.Fatalf("ownerRef = %v, want 422 principal_not_allowed", err)
	}
}
