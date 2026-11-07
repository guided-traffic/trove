package token

import "testing"

// TestR_AUTH_037_AnonymousSwitchOrder: off grants nothing that on does not, so only a switch that ends off or stays as it is
// narrows.
func TestR_AUTH_037_AnonymousSwitchOrder(t *testing.T) {
	for _, c := range []struct{ current, after, want bool }{
		{true, false, true}, {false, false, true}, {true, true, true}, {false, true, false},
	} {
		if got := (switchOrder{}).Narrows(c.current, c.after); got != c.want {
			t.Errorf("Narrows(%v, %v) = %v, want %v", c.current, c.after, got, c.want)
		}
	}
}

func TestR_AUTH_029_ReplayReasonsAreTheColumnsValues(t *testing.T) {
	for _, ok := range []string{ReasonOwner, ReasonAdmin, ReasonRotated, ReasonNPMLogout, ReasonLeaked, ReasonPrincipalDisabled, ReasonParentRevoked, "scim", "mapping_deleted"} {
		if !validReplayReason(ok) {
			t.Errorf("%q must be accepted", ok)
		}
	}
	if validReplayReason("forged") || validReplayReason("") {
		t.Error("a reason outside the column's list must not be stored")
	}
}
