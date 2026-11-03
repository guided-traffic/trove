package roletest_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/roletest"
)

// TestR_BAS_048_TheWholeRoleServesAColdMissAWarmHitARevocationAndTheOutage_Volume: the role started from a
// configuration file against the in-process main installation over TLS, with the volume backend.
func TestR_BAS_048_TheWholeRoleServesAColdMissAWarmHitARevocationAndTheOutage_Volume(t *testing.T) {
	b := roletest.Volume()
	b.Client = &http.Client{Timeout: 10 * time.Second}
	roletest.Run(t, b)
}
