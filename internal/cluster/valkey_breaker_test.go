package cluster

import (
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func TestBreaker(t *testing.T) {
	newB := func() (*breaker, *fakeClock, *[]bool) {
		clk := &fakeClock{t: time.Unix(1000, 0)}
		var changes []bool
		b := newBreaker(3, 5*time.Second, clk.now, func(open bool) { changes = append(changes, open) })
		return b, clk, &changes
	}
	fail := func(b *breaker) {
		t.Helper()
		if !b.allow() {
			t.Fatal("call refused while the breaker should pass it")
		}
		b.done(outcomeFailure)
	}

	t.Run("R-HA-057 opens after the threshold of consecutive failures", func(t *testing.T) {
		b, _, changes := newB()
		fail(b)
		fail(b)
		if b.isOpen() {
			t.Fatal("open after 2 of 3 failures")
		}
		fail(b)
		if !b.isOpen() || b.allow() {
			t.Fatal("not open, or still passing calls, after 3 failures")
		}
		if len(*changes) != 1 || !(*changes)[0] {
			t.Fatalf("changes = %v, want one open", *changes)
		}
	})
	t.Run("R-HA-057 a success resets the consecutive count", func(t *testing.T) {
		b, _, _ := newB()
		fail(b)
		fail(b)
		b.allow()
		b.done(outcomeSuccess)
		fail(b)
		fail(b)
		if b.isOpen() {
			t.Fatal("open although successes interrupted the failures")
		}
	})
	t.Run("R-HA-057 lets one probe through after open_duration and closes on success", func(t *testing.T) {
		b, clk, changes := newB()
		for range 3 {
			fail(b)
		}
		clk.advance(4 * time.Second)
		if b.allow() {
			t.Fatal("call passed before open_duration")
		}
		clk.advance(2 * time.Second)
		if !b.allow() {
			t.Fatal("probe refused after open_duration")
		}
		if b.allow() {
			t.Fatal("a second call passed while the probe is out")
		}
		b.done(outcomeSuccess)
		if b.isOpen() || !b.allow() {
			t.Fatal("breaker did not close after a successful probe")
		}
		if got := *changes; len(got) != 2 || !got[0] || got[1] {
			t.Fatalf("changes = %v, want open then closed", got)
		}
	})
	t.Run("R-HA-057 a failed probe re-opens for another open_duration", func(t *testing.T) {
		b, clk, _ := newB()
		for range 3 {
			fail(b)
		}
		clk.advance(6 * time.Second)
		fail(b) // the probe
		if b.allow() {
			t.Fatal("call passed right after a failed probe")
		}
		clk.advance(6 * time.Second)
		if !b.allow() {
			t.Fatal("no probe after the second open_duration")
		}
	})
	t.Run("R-HA-057 a neutral outcome neither counts nor wedges a half-open breaker", func(t *testing.T) {
		b, clk, _ := newB()
		fail(b)
		fail(b)
		b.allow()
		b.done(outcomeNeutral)
		fail(b) // third consecutive failure: neutral did not reset
		if !b.isOpen() {
			t.Fatal("neutral outcome reset the failure count")
		}
		clk.advance(6 * time.Second)
		if !b.allow() {
			t.Fatal("no probe")
		}
		b.done(outcomeNeutral)
		if !b.allow() {
			t.Fatal("a cancelled probe wedged the breaker")
		}
	})
}
