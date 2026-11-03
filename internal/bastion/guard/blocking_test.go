package guard

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/shared"
)

// slowBlocks is a block list whose Set waits for a release, the way a Valkey round trip waits.
type slowBlocks struct {
	shared.BlockList
	entered chan string
	release chan struct{}
	sets    atomic.Int32
}

func (s *slowBlocks) Set(ctx context.Context, addr string, d time.Duration, level int) error {
	s.sets.Add(1)
	s.entered <- addr
	<-s.release
	return s.BlockList.Set(ctx, addr, d, level)
}

// The decision to block one address does not hold another address back (Q-BAS-128): while the block of one
// address waits on the shared state, the refusals of another address are decided.
func TestR_BAS_037_ABlockInFlightForOneAddressDoesNotHoldAnotherBack(t *testing.T) {
	r := newRig(t, 1, func(o *Options) { o.Blocking.Threshold = 1 })
	slow := &slowBlocks{BlockList: r.st.Blocks, entered: make(chan string, 2), release: make(chan struct{})}
	r.st.Blocks = slow
	const a, b = "198.51.100.1", "198.51.100.2"

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 5 {
			r.g.refused(ctx, a, "rate_limited")
		}
	}()
	if got := <-slow.entered; got != a {
		t.Fatalf("the first block in flight is %s, want %s", got, a)
	}

	second := make(chan struct{})
	go func() {
		defer close(second)
		for range 5 {
			r.g.refused(ctx, b, "rate_limited")
		}
	}()
	select {
	case got := <-slow.entered:
		if got != b {
			t.Fatalf("the second block in flight is %s, want %s", got, b)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the block of another address waits for the block of the first")
	}
	close(slow.release)
	<-done
	<-second
	if n := r.g.blocking.held(); n != 0 {
		t.Fatalf("%d addresses stay locked", n)
	}
	for _, addr := range []string{a, b} {
		if r.g.Blocked(ctx, addr).Allowed {
			t.Fatalf("%s is not blocked", addr)
		}
	}
}

// The refusals of one address that cross the threshold together set one block, not several levels (R-BAS-037).
func TestR_BAS_037_ConcurrentRefusalsOfOneAddressSetOneBlock(t *testing.T) {
	r := newRig(t, 1, func(o *Options) { o.Blocking.Threshold = 3 })
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.g.refused(ctx, "198.51.100.3", "rate_limited")
		}()
	}
	wg.Wait()
	if got := value(r.m.Blocks.WithLabelValues("refusals")); got != 1 {
		t.Fatalf("trove_bastion_blocks_total = %v, want 1", got)
	}
	if lv, _ := r.st.Blocks.Level(ctx, "198.51.100.3"); lv != 1 {
		t.Fatalf("level = %d, want 1", lv)
	}
}
