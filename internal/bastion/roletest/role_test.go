package roletest_test

import (
	"testing"

	"github.com/guided-traffic/trove/internal/bastion/roletest"
)

// TestR_BAS_048_TheWholeRoleServesAColdMissAWarmHitARevocationAndTheOutage_Volume: the role started from a
// configuration file against the in-process main installation over TLS, with the volume backend.
func TestR_BAS_048_TheWholeRoleServesAColdMissAWarmHitARevocationAndTheOutage_Volume(t *testing.T) {
	roletest.Run(t, roletest.Volume())
}
