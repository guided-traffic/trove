package guard

import (
	"testing"
	"time"
)

// A client slightly over its miss limit keeps its budget and is not blocked: a refused miss does not consume
// the limit, and 30 refusals a minute are far from the threshold of a flood (Q-BAS-127). This is the
// regression test of the proof of concept in which, at 330 misses a minute against a limit of 300, no miss was
// admitted from the second minute on and the address was blocked after it.
func TestR_BAS_035_R_BAS_037_AClientSlightlyOverTheMissLimitKeepsItsBudgetAndIsNotBlocked(t *testing.T) {
	r := newRig(t, 1, nil)
	const addr = "198.51.100.7"
	const perMinute = 330 // the limit is 300
	step := time.Minute / perMinute
	for minute := 1; minute <= 5; minute++ {
		admitted, blocked := 0, 0
		for i := 0; i < perMinute; i++ {
			if !r.g.Blocked(ctx, addr).Allowed {
				blocked++
			} else if r.g.Miss(ctx, addr, true).Allowed {
				admitted++
			}
			r.clk.advance(step)
		}
		if blocked != 0 {
			t.Fatalf("minute %d: %d requests answered address_blocked for a client at 1.1 x its miss limit", minute, blocked)
		}
		if admitted < 290 || admitted > 310 {
			t.Fatalf("minute %d: %d of %d misses admitted, want about the limit of 300", minute, admitted, perMinute)
		}
	}
}

// The same for the request limit: a client slightly over its rate is admitted up to its rate, not starved.
func TestR_BAS_035_R_BAS_037_AClientSlightlyOverTheRequestRateKeepsItsRateAndIsNotBlocked(t *testing.T) {
	r := newRig(t, 1, nil)
	const addr = "198.51.100.8"
	step := time.Second / 11 // anonymous: 10 per second, burst 40
	for minute := 1; minute <= 4; minute++ {
		admitted := 0
		for i := 0; i < 11*60; i++ {
			if !r.g.Blocked(ctx, addr).Allowed {
				t.Fatalf("minute %d: a client at 1.1 x its request rate was blocked", minute)
			}
			if r.g.Request(ctx, addr, false).Allowed {
				admitted++
			}
			r.clk.advance(step)
		}
		if minute > 1 && admitted < 590 {
			t.Fatalf("minute %d: %d of 660 requests admitted, want about 600", minute, admitted)
		}
	}
}

// A flood stays the answer to a block: refusals well above the threshold still block (R-BAS-037).
func TestR_BAS_037_AFloodOfMissesStillBlocks(t *testing.T) {
	r := newRig(t, 1, nil)
	const addr = "198.51.100.9"
	for i := 0; i < 3000; i++ { // anonymous: 30 a minute, 3,000 in 30 s
		r.g.Miss(ctx, addr, false)
		r.clk.advance(10 * time.Millisecond)
	}
	if r.g.Blocked(ctx, addr).Allowed {
		t.Fatal("3,000 misses in 30 s did not block the address")
	}
}
