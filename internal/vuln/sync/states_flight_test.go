package sync

import (
	"context"
	"errors"
	"log/slog"
	stdsync "sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/store"
)

func testStates(t *testing.T) (*States, *cache.Registry, func() time.Time) {
	t.Helper()
	reg, err := cache.NewRegistry(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	return newStates(nil, reg, 24*time.Hour, clock, slog.New(slog.DiscardHandler)), reg, clock
}

// R-VULN-027, R-ARCH-032: the callers of a miss share one refill (single-flight), not one query each.
func TestR_VULN_027_ConcurrentMissesShareOneRefill(t *testing.T) {
	s, _, clock := testStates(t)
	var reads atomic.Int32
	release := make(chan struct{})
	s.list = func(context.Context) ([]store.StateSummary, error) {
		reads.Add(1)
		<-release
		return []store.StateSummary{{Ecosystem: "npm", Complete: true, DataEpoch: 2, SyncedAt: clock()}}, nil
	}
	var wg stdsync.WaitGroup
	for range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if st := s.State("npm"); !st.Complete {
				t.Errorf("state %+v", st)
			}
		}()
	}
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()
	if n := reads.Load(); n != 1 {
		t.Errorf("%d reads for 40 concurrent callers of one miss", n)
	}
}

// R-VULN-015, R-ARCH-032: once the replica holds a state, a slow database does not hold a download for the load timeout: the
// caller answers with the last state after refreshWait while the refill goes on.
func TestR_VULN_027_ASlowRefillDoesNotHoldACallerThatHasAState(t *testing.T) {
	s, reg, clock := testStates(t)
	block := make(chan struct{})
	var slow atomic.Bool
	s.list = func(ctx context.Context) ([]store.StateSummary, error) {
		if slow.Load() {
			select {
			case <-block:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return []store.StateSummary{{Ecosystem: "npm", Complete: true, DataEpoch: 1, SyncedAt: clock()}}, nil
	}
	if st := s.State("npm"); !st.Complete {
		t.Fatalf("first state %+v", st)
	}
	slow.Store(true)
	reg.ApplyChange(cluster.AreaVulnData, vuln.DataChangePayloadVersion, nil)
	start := time.Now()
	st := s.State("npm")
	took := time.Since(start)
	close(block)
	if !st.Complete || st.Epoch != 1 {
		t.Errorf("the last state was not served: %+v", st)
	}
	if took > 2*time.Second {
		t.Errorf("the caller waited %s for a slow refill", took)
	}
}

// A failed read is not retried by every caller: within retryAfter the callers answer with the last state without a query.
func TestR_VULN_027_AFailedReadIsNotRetriedPerCaller(t *testing.T) {
	s, reg, clock := testStates(t)
	var reads atomic.Int32
	var fail atomic.Bool
	s.list = func(context.Context) ([]store.StateSummary, error) {
		reads.Add(1)
		if fail.Load() {
			return nil, errors.New("database down")
		}
		return []store.StateSummary{{Ecosystem: "npm", Complete: true, SyncedAt: clock()}}, nil
	}
	s.State("npm")
	fail.Store(true)
	reg.ApplyChange(cluster.AreaVulnData, vuln.DataChangePayloadVersion, nil)
	for range 20 {
		if st := s.State("npm"); !st.Complete {
			t.Fatalf("the last state was lost: %+v", st)
		}
	}
	if n := reads.Load(); n != 2 {
		t.Errorf("%d reads, want the first and one failed refill", n)
	}
}
