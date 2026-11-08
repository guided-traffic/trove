package proxy

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
)

// emptySlots fails the test when a counter of the slots is left over.
func emptySlots(t *testing.T, s *slots) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inUse != 0 || len(s.repo) != 0 || len(s.bg) != 0 || len(s.runs) != 0 {
		t.Fatalf("counters left over: inUse %d, repo %v, bg %v, runs %v", s.inUse, s.repo, s.bg, s.runs)
	}
}

func TestR_REPO_040_TheRepositorysShareIsReturnedWhenTheExchangeEnds(t *testing.T) {
	ctx := context.Background()
	s := newSlots(10, nil)
	first, _, err := s.acquire(ctx, 1, 1, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	// The repository's share is one: a second fetch of it queues until the first exchange ends.
	if _, _, err := s.acquire(ctx, 1, 1, 100*time.Millisecond); err == nil {
		t.Fatal("a second fetch got a slot above the repository's share")
	}
	first.exchanged()
	start := time.Now()
	second, _, err := s.acquire(ctx, 1, 1, time.Second)
	if err != nil {
		t.Fatalf("no slot after the exchange ended: %v", err)
	}
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Fatalf("the second fetch waited %v after the exchange ended", d)
	}
	if got := s.held(); got != 2 {
		t.Fatalf("held = %d, want 2 until the first fetch ends", got)
	}
	// Repeated calls change no counter.
	first.exchanged()
	first.exchanged()
	if got := s.held(); got != 2 {
		t.Fatalf("held = %d after a repeated exchanged", got)
	}
	s.mu.Lock()
	if s.repo[1] != 1 {
		t.Fatalf("repo[1] = %d, want the second fetch's 1", s.repo[1])
	}
	s.mu.Unlock()
	first.release()
	first.release()
	if got := s.held(); got != 1 {
		t.Fatalf("held = %d after the first release, want 1", got)
	}
	s.mu.Lock()
	if s.repo[1] != 1 {
		t.Fatalf("repo[1] = %d after the first release, want 1", s.repo[1])
	}
	s.mu.Unlock()
	second.release() // without a prior exchanged: returns both parts
	second.release()
	if got := s.held(); got != 0 {
		t.Fatalf("held = %d at the end", got)
	}
	emptySlots(t, s)
}

func TestR_REPO_040_TheReplicasSlotIsHeldUntilTheFetchEnds(t *testing.T) {
	ctx := context.Background()
	t.Run("the next fetch of another repository waits for the release", func(t *testing.T) {
		s := newSlots(1, nil)
		hold, _, err := s.acquire(ctx, 1, 8, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		hold.exchanged()
		got := make(chan error, 1)
		var next *slotHold
		go func() {
			var err error
			next, _, err = s.acquire(ctx, 2, 8, 5*time.Second)
			got <- err
		}()
		select {
		case err := <-got:
			t.Fatalf("a fetch got the replica's slot while the first fetch was storing: %v", err)
		case <-time.After(100 * time.Millisecond):
		}
		hold.release()
		select {
		case err := <-got:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(50 * time.Millisecond):
			t.Fatal("the waiting fetch did not take the slot after the release")
		}
		next.release()
		emptySlots(t, s)
	})
	t.Run("without a release the queue times out", func(t *testing.T) {
		s := newSlots(1, nil)
		hold, _, err := s.acquire(ctx, 1, 8, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		hold.exchanged()
		_, _, err = s.acquire(ctx, 2, 8, 200*time.Millisecond)
		if ee, ok := errs.As(err); !ok || ee.Code != errs.CodeFetchQueueTimeout {
			t.Fatalf("err = %v, want %s", err, errs.CodeFetchQueueTimeout)
		}
		hold.release()
		emptySlots(t, s)
	})
}

func TestR_REPO_055_ABackgroundFetchPastItsExchangeIsNotPreempted(t *testing.T) {
	ctx := context.Background()
	t.Run("a fetch that is storing is neither pre-empted by a client nor by the signal", func(t *testing.T) {
		s := newSlots(10, nil)
		bctx, bg, err := s.acquireBackground(ctx, 1, 2, 1) // budget 2: the background share is 1
		if err != nil {
			t.Fatal(err)
		}
		if got := s.background(1); got != 1 {
			t.Fatalf("background = %d before the exchange", got)
		}
		bg.exchanged()
		if got := s.background(1); got != 0 {
			t.Fatalf("background = %d after the exchange, want 0", got)
		}
		var clients []*slotHold
		for range 2 {
			c, _, err := s.acquire(ctx, 1, 2, 50*time.Millisecond)
			if err != nil {
				t.Fatalf("a client fetch found no slot: %v", err)
			}
			clients = append(clients, c)
		}
		if cause := context.Cause(bctx); cause != nil {
			t.Fatalf("the storing fetch was cancelled: %v", cause)
		}
		if n := s.yield(1); n != 0 {
			t.Fatalf("yield pre-empted %d fetches", n)
		}
		select {
		case <-bctx.Done():
			t.Fatal("the context of the storing fetch ended with the yield")
		default:
		}
		for _, c := range clients {
			c.release()
		}
		bg.release()
		select {
		case <-bctx.Done():
		default:
			t.Fatal("the context did not end with the release")
		}
		if cause := context.Cause(bctx); cause == nil || errors.Is(cause, ErrPreempted) {
			t.Fatalf("cause = %v, want a release without the pre-emption", cause)
		}
		emptySlots(t, s)
	})
	t.Run("the invariants hold through the parts", func(t *testing.T) {
		s := newSlots(10, nil)
		_, bg, _ := s.acquireBackground(ctx, 1, 8, 2)
		client, _, _ := s.acquire(ctx, 1, 8, time.Second)
		check := func(step string, inUse, repo, bgN, runs int) {
			t.Helper()
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.inUse != inUse || s.repo[1] != repo || s.bg[1] != bgN || len(s.runs[1]) != runs {
				t.Fatalf("%s: inUse %d, repo %d, bg %d, runs %d; want %d, %d, %d, %d",
					step, s.inUse, s.repo[1], s.bg[1], len(s.runs[1]), inUse, repo, bgN, runs)
			}
		}
		check("both held", 2, 2, 1, 1)
		bg.exchanged()
		check("background exchanged", 2, 1, 0, 0)
		client.exchanged()
		check("client exchanged", 2, 0, 0, 0)
		bg.release()
		check("background released", 1, 0, 0, 0)
		client.release()
		check("client released", 0, 0, 0, 0)
		emptySlots(t, s)
	})
	t.Run("a hold pre-empted before its exchange changes no counter afterwards", func(t *testing.T) {
		s := newSlots(10, nil)
		bctx, bg, _ := s.acquireBackground(ctx, 1, 2, 1)
		if n := s.yield(1); n != 1 {
			t.Fatalf("yield pre-empted %d, want 1", n)
		}
		if !errors.Is(context.Cause(bctx), ErrPreempted) {
			t.Fatalf("cause = %v", context.Cause(bctx))
		}
		other, _, err := s.acquire(ctx, 1, 2, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		bg.exchanged()
		bg.release()
		bg.release()
		if got := s.held(); got != 1 {
			t.Fatalf("held = %d, want the other fetch's 1", got)
		}
		s.mu.Lock()
		if s.repo[1] != 1 || s.bg[1] != 0 {
			t.Fatalf("repo %d, bg %d", s.repo[1], s.bg[1])
		}
		s.mu.Unlock()
		other.release()
		emptySlots(t, s)
	})
}
