package engine

import (
	"testing"
	"time"
)

// TestR_BAS_025_TheFloorOfAPassThroughStartsAfterTheGracePeriodAndIsAnAverage: a transfer is too slow once it is
// older than the grace period and has moved fewer bytes than the floor rate asks for since it began.
func TestR_BAS_025_TheFloorOfAPassThroughStartsAfterTheGracePeriodAndIsAnAverage(t *testing.T) {
	s := newSiteHarness(t, options{})
	s.e.paceGrace, s.e.paceMinRate = 10*time.Second, 1000
	for _, c := range []struct {
		age  time.Duration
		sent int64
		slow bool
	}{
		{5 * time.Second, 0, false},      // inside the grace period nothing counts
		{20 * time.Second, 5000, true},   // 250 B/s against 1000 B/s
		{20 * time.Second, 25000, false}, // 1250 B/s
		{200 * time.Second, 25000, true}, // an average, not a burst: the early bytes do not pay for the later time
	} {
		if got := s.e.tooSlow(time.Now().Add(-c.age), c.sent); got != c.slow {
			t.Errorf("after %v with %d bytes: tooSlow = %v, want %v", c.age, c.sent, got, c.slow)
		}
	}
}
