package proxy

import (
	"testing"
	"time"
)

// TestYieldWindowsAreBounded: a signal names any repository id, so the windows a replica keeps are purged when they have passed and
// capped, and a forged flood does not grow the replica's memory (R-REPO-055, Q-REPO-182, H-526).
func TestYieldWindowsAreBounded(t *testing.T) {
	clk := newFakeClock()
	s := newSlots(10, nil)
	s.now = clk.now
	t.Run("R-REPO-055 ids that were never repositories are held at most 4096 at a time", func(t *testing.T) {
		for id := int64(1); id <= 6000; id++ {
			s.yield(id)
		}
		if n := len(s.yielding); n > 4096 {
			t.Fatalf("%d windows are held", n)
		}
	})
	t.Run("R-REPO-055 a window that has passed is dropped when the next signal arrives", func(t *testing.T) {
		clk.advance(31 * time.Second)
		s.yield(7)
		if n := len(s.yielding); n != 1 {
			t.Fatalf("%d windows are held after the old ones passed", n)
		}
	})
	t.Run("R-REPO-055 a repository that holds a window keeps it moving when the bound is reached", func(t *testing.T) {
		for id := int64(100); id < 100+5000; id++ {
			s.yield(id)
		}
		clk.advance(20 * time.Second)
		s.yield(7)
		clk.advance(15 * time.Second) // 35 s after the first signal of 7 (it was re-set), the others have passed
		s.yield(8)
		s.mu.Lock()
		open := s.yieldOpen(7, clk.now())
		s.mu.Unlock()
		if !open {
			t.Fatal("the window of a repository that signalled 15 s ago is closed")
		}
	})
}
