package cluster

import (
	"context"
	"testing"
	"time"
)

// TestR_BAS_016_ARequestIsChargedItsUnits: TakeNAt counts n units of one request in the shared window (one round
// trip) and in the fallback bucket, admits it only when the window holds them all, and names a Retry-After after
// which n units would fit (R-ARCH-034, Q-BAS-117). A request above the burst is never admitted, and one above
// MaxTakeUnits is refused without being counted.
func TestR_BAS_016_ARequestIsChargedItsUnits(t *testing.T) {
	ctx := context.Background()
	t.Run("in the cluster window", func(t *testing.T) {
		st, clk := newMemWindows(), newLimClock()
		l := replica(t, st, clk, 1, nil).NewLimiter("bastion.download_events", ScopeCluster, 2, 20)
		for i := range 2 {
			if v := l.TakeNAt(ctx, "b", 10, time.Time{}); !v.Allowed || v.Mode != ModeCluster {
				t.Fatalf("batch %d: %+v", i, v)
			}
		}
		if st.calls != 2 {
			t.Fatalf("%d store calls for two batches: a batch is one round trip", st.calls)
		}
		v := l.TakeNAt(ctx, "b", 10, time.Time{})
		if v.Allowed || v.RetryAfter < 5*time.Second {
			t.Fatalf("a third batch in the window: %+v", v)
		}
		if v := l.TakeNAt(ctx, "other", 10, time.Time{}); !v.Allowed {
			t.Fatalf("another bucket: %+v", v)
		}
		clk.advance(v.RetryAfter + 2*l.Window())
		if v := l.TakeNAt(ctx, "b", 10, time.Time{}); !v.Allowed {
			t.Fatalf("after the Retry-After and the window: %+v", v)
		}
		if v := l.TakeNAt(ctx, "c", 21, time.Time{}); v.Allowed {
			t.Fatalf("a request above the burst: %+v", v)
		}
		calls := st.calls
		if v := l.TakeNAt(ctx, "d", MaxTakeUnits+1, time.Time{}); v.Allowed || st.calls != calls {
			t.Fatalf("a request above MaxTakeUnits: %+v, %d store calls", v, st.calls-calls)
		}
	})
	t.Run("in the fallback bucket", func(t *testing.T) {
		st, clk := newMemWindows(), newLimClock()
		st.down = true
		l := replica(t, st, clk, 2, nil).NewLimiter("bastion.download_events", ScopeCluster, 2, 20)
		if v := l.TakeNAt(ctx, "b", 10, time.Time{}); !v.Allowed || v.Mode != ModeFallback {
			t.Fatalf("a batch within the replica's share: %+v", v)
		}
		v := l.TakeNAt(ctx, "b", 1, time.Time{})
		if v.Allowed {
			t.Fatalf("the share of two replicas holds 10 units: %+v", v)
		}
		if v := l.TakeNAt(ctx, "b", 0, time.Time{}); v.Allowed {
			t.Fatalf("zero units count as one: %+v", v)
		}
	})
}
