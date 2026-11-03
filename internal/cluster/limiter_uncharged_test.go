package cluster

import (
	"context"
	"errors"
	"testing"
	"time"
)

// hitNet and refuse make memWindows an unchargedStore: the refusals live in counters of the limiter named
// <limiter>-refused, the way the Valkey store keeps them.
func (m *memWindows) hitNet(ctx context.Context, l, b string, idx int64, ttl time.Duration) (int64, int64, error) {
	cur, prev, err := m.hit(ctx, l, b, idx, ttl)
	if err != nil {
		return 0, 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return max(0, cur-m.counts[m.key(l+refusedSuffix, b, idx)]), max(0, prev-m.counts[m.key(l+refusedSuffix, b, idx-1)]), nil
}

func (m *memWindows) refuse(ctx context.Context, l, b string, idx int64, ttl time.Duration) error {
	return m.count(ctx, l+refusedSuffix, b, idx, ttl)
}

func TestR_BAS_035_AnUnchargedLimitAdmitsUpToItsRateWhateverIsRefused(t *testing.T) {
	st, clk := newMemWindows(), newLimClock()
	l := replica(t, st, clk, 1, nil).NewLimiter("bastion.requests", ScopeCluster, 10, 40)
	ctx := t.Context()
	// 11 requests per second for a minute: the charged form (Take) starves after the burst, this one admits
	// the rate.
	charged := replica(t, newMemWindows(), clk, 1, nil).NewLimiter("bastion.charged", ScopeCluster, 10, 40)
	admitted, chargedAdmitted := 0, 0
	for range 11 * 60 {
		if l.TakeUncharged(ctx, "a").Allowed {
			admitted++
		}
		if charged.Take(ctx, "a").Allowed {
			chargedAdmitted++
		}
		clk.advance(time.Second / 11)
	}
	if admitted < 580 || admitted > 660 {
		t.Fatalf("%d of 660 admitted by the uncharged limit, want about 600", admitted)
	}
	if chargedAdmitted > 100 {
		t.Fatalf("the charged form admitted %d: the comparison no longer shows the starvation it stands for", chargedAdmitted)
	}
}

func TestR_BAS_035_AnUnchargedRefusalCarriesARetryAfterThatIsEnough(t *testing.T) {
	st, clk := newMemWindows(), newLimClock()
	l := replica(t, st, clk, 1, nil).NewLimiter("bastion.requests", ScopeCluster, 1, 4)
	ctx := t.Context()
	for range 4 {
		if !l.TakeUncharged(ctx, "a").Allowed {
			t.Fatal("the burst was refused")
		}
	}
	v := l.TakeUncharged(ctx, "a")
	if v.Allowed || v.RetryAfter < time.Second {
		t.Fatalf("verdict = %+v, want a refusal with a Retry-After of one second or more", v)
	}
	clk.advance(v.RetryAfter)
	if !l.TakeUncharged(ctx, "a").Allowed {
		t.Fatalf("still refused after the Retry-After of %s", v.RetryAfter)
	}
}

func TestR_BAS_035_EstimateWithinKeepsOnlyWhatItAdmits(t *testing.T) {
	st, clk := newMemWindows(), newLimClock()
	l := replica(t, st, clk, 1, nil).NewLimiter("bastion.misses", ScopeCluster, 1000, 60_000)
	ctx := t.Context()
	admitted := 0
	for range 100 {
		ok, n, err := l.EstimateWithin(ctx, "a", 30)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			admitted++
		} else if n <= 30 {
			t.Fatalf("refused at %d within the limit 30", n)
		}
	}
	if admitted != 30 {
		t.Fatalf("%d admitted in one window, want 30", admitted)
	}
	// The next window starts from the 30 that were admitted, not from the 100 that were asked.
	clk.advance(time.Minute)
	if n, _ := l.Estimate(ctx, "b", false); n != 0 {
		t.Fatalf("an unseen key counts %d", n)
	}
	ok, n, _ := l.EstimateWithin(ctx, "a", 30)
	if ok || n != 31 {
		t.Fatalf("first request of the next window: ok=%v n=%d, want refused at 31 (30 x (1-0) + 1)", ok, n)
	}
	clk.advance(30 * time.Second)
	if ok, _, _ := l.EstimateWithin(ctx, "a", 30); !ok {
		t.Fatal("half a window later the estimate has not fallen")
	}
}

func TestR_BAS_035_AnUnchargedLimitFallsBackAndNeverFailsTheRequest(t *testing.T) {
	st, clk := newMemWindows(), newLimClock()
	l := replica(t, st, clk, 2, nil).NewLimiter("bastion.requests", ScopeCluster, 10, 40)
	ctx := t.Context()
	st.fail = true
	if v := l.TakeUncharged(ctx, "a"); v.Mode != ModeFallback || !v.Allowed {
		t.Fatalf("verdict = %+v, want an admission by the fallback", v)
	}
	if _, _, err := l.EstimateWithin(ctx, "a", 5); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("EstimateWithin err = %v, want ErrUnavailable", err)
	}
	// A store without counters of refusals, and a limiter of the replica scope: Take, and never Valkey.
	bare := replica(t, bareStore{}, clk, 1, nil).NewLimiter("bastion.bare", ScopeCluster, 10, 40)
	if v := bare.TakeUncharged(ctx, "a"); !v.Allowed {
		t.Fatalf("verdict = %+v", v)
	}
	if _, _, err := bare.EstimateWithin(ctx, "a", 5); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

// bareStore is a windowStore without the counters of refusals.
type bareStore struct{}

func (bareStore) hit(context.Context, string, string, int64, time.Duration) (int64, int64, error) {
	return 1, 0, nil
}

func (bareStore) read(context.Context, string, string, int64, time.Duration) (int64, int64, error) {
	return 0, 0, nil
}
func (bareStore) count(context.Context, string, string, int64, time.Duration) error { return nil }
func (bareStore) available() bool                                                   { return true }
func (bareStore) degraded()                                                         {}
