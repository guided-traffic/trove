//go:build integration

package bastionshared

import (
	"testing"
	"time"
)

// A client slightly over its limits keeps being admitted at them with Valkey, on both replicas, and is not
// blocked: a refused request or miss gives its count back (Q-BAS-127). The first minute only fills the window,
// so the bounds hold from the second.
func TestR_BAS_035_R_BAS_037_WithValkeyAClientSlightlyOverItsLimitsKeepsItsRateAndIsNotBlocked(t *testing.T) {
	s := newSetup(t)
	a, b := s.pair(t)
	reqAddr, missAddr := address(), address()
	const perMinuteRequests = 11 * 60 // anonymous: 10 per second
	// One request every step and one miss every other step: 330 misses a minute against the 300 of the
	// authenticated class.
	for minute := 1; minute <= 4; minute++ {
		reqAdmitted, missAdmitted := 0, 0
		for i := 0; i < perMinuteRequests; i++ {
			r := a
			if i%2 == 1 {
				r = b
			}
			if v := r.g.Blocked(ctx, reqAddr); !v.Allowed {
				t.Fatalf("minute %d: a client at 1.1 x its request rate was blocked", minute)
			}
			if r.g.Request(ctx, reqAddr, false).Allowed {
				reqAdmitted++
			}
			if i%2 == 0 {
				if v := r.g.Blocked(ctx, missAddr); !v.Allowed {
					t.Fatalf("minute %d: a client at 1.1 x its miss limit was blocked", minute)
				}
				if r.g.Miss(ctx, missAddr, true).Allowed {
					missAdmitted++
				}
			}
			s.clk.advance(time.Minute / perMinuteRequests)
		}
		if minute == 1 {
			continue
		}
		// 10 per second over a minute is 600; the sliding window admits about that.
		if reqAdmitted < 560 || reqAdmitted > 650 {
			t.Fatalf("minute %d: %d of %d requests admitted, want about 600", minute, reqAdmitted, perMinuteRequests)
		}
		if missAdmitted < 280 || missAdmitted > 310 {
			t.Fatalf("minute %d: %d of 330 misses admitted, want about 300", minute, missAdmitted)
		}
	}
}
