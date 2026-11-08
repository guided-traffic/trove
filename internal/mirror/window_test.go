package mirror

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/mirror/discover"
)

// limited is a rate-limit answer for the tests.
type limited struct {
	after  time.Duration
	hinted bool
}

func (l *limited) Error() string                     { return "limited" }
func (l *limited) RetryAfter() (time.Duration, bool) { return l.after, l.hinted }
func (l *limited) RateLimitReason() string           { return "retry_after" }

func newTestWindow(c Clock, ceiling int) *Window { return NewWindow(c, fixedJitter(0.5), ceiling) }

func okRequest(context.Context) error { return nil }

func failing(err error) func(context.Context) error {
	return func(context.Context) error { return err }
}

func sleptTotal(c *fakeClock) (total time.Duration) {
	for _, d := range c.slept() {
		total += d
	}
	return total
}

func TestR_REPO_054_AWindowOutsideAPassStartsAtOneAndGrowsByDoubling(t *testing.T) {
	w := newTestWindow(newClock(), 8)
	if w.Size() != 1 {
		t.Fatalf("start window = %d", w.Size())
	}
	for i, want := range []int{2, 2, 4, 4, 4, 4, 8} {
		if err := w.Do(t.Context(), okRequest); err != nil {
			t.Fatal(err)
		}
		if w.Size() != want {
			t.Fatalf("after success %d the window is %d, want %d", i+1, w.Size(), want)
		}
	}
	for range 10 {
		_ = w.Do(t.Context(), okRequest)
	}
	if w.Size() != 8 {
		t.Fatalf("the window passed its ceiling: %d", w.Size())
	}
}

func TestR_REPO_054_ARateLimitAnswerWaitsItsHintHalvesTheWindowAndRepeatsTheRequest(t *testing.T) {
	c := newClock()
	w := newTestWindow(c, 8)
	for range 3 {
		_ = w.Do(t.Context(), okRequest) // window 1 -> 2 -> 4
	}
	if w.Size() != 4 {
		t.Fatalf("window = %d", w.Size())
	}
	var calls atomic.Int32
	err := w.Do(t.Context(), func(context.Context) error {
		if calls.Add(1) == 1 {
			return &limited{after: 7 * time.Second, hinted: true}
		}
		return nil
	})
	if err != nil || calls.Load() != 2 {
		t.Fatalf("err %v after %d calls", err, calls.Load())
	}
	if got := sleptTotal(c); got != 7*time.Second {
		t.Fatalf("slept %v, want the hint of 7s", got)
	}
	if w.Size() != 2 {
		t.Fatalf("the window is %d, want it halved to 2", w.Size())
	}
}

func TestR_REPO_054_ARateLimitAnswerWithoutAHintWaitsTheBackoff(t *testing.T) {
	c := newClock()
	w := newTestWindow(c, 4)
	var calls atomic.Int32
	err := w.Do(t.Context(), func(context.Context) error {
		if calls.Add(1) <= 2 {
			return &limited{}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// d = 1 s then 2 s; the draw 0.5 gives 0.75 d, and a wait is at least one second.
	got := c.slept()
	if len(got) != 2 || got[0] != time.Second || got[1] != 1500*time.Millisecond {
		t.Fatalf("waits %v", got)
	}
	if w.Size() != 2 { // halved to 1 by the first answer, then one step of the linear growth after the success
		t.Fatalf("window %d", w.Size())
	}
}

func TestR_REPO_054_AWaitBeyondTheLongWaitEndsTheRunWithADeferredError(t *testing.T) {
	c := newClock()
	w := newTestWindow(c, 4)
	var calls atomic.Int32
	err := w.Do(t.Context(), func(context.Context) error {
		calls.Add(1)
		return &limited{after: 3600 * time.Second, hinted: true}
	})
	var d *DeferredError
	if !errors.As(err, &d) {
		t.Fatalf("%v", err)
	}
	if want := c.Now().Add(time.Hour); !d.Until.Equal(want) {
		t.Fatalf("until %v, want %v", d.Until, want)
	}
	if _, ok := discover.AsRateLimited(err); !ok {
		t.Fatal("the deferred error hides the rate-limit answer")
	}
	if len(c.slept()) != 0 {
		t.Fatalf("slept through a long wait: %v", c.slept())
	}
	// Every later call gets the same answer and sends nothing.
	if err := w.Do(t.Context(), okRequest); !errors.As(err, &d) || calls.Load() != 1 {
		t.Fatalf("%v after %d calls", err, calls.Load())
	}
}

func TestR_REPO_055_APreemptionAndAWaitAreRepeatedAndAnyOtherErrorIsReturned(t *testing.T) {
	c := newClock()
	w := newTestWindow(c, 4)
	var calls atomic.Int32
	err := w.Do(t.Context(), func(context.Context) error {
		switch calls.Add(1) {
		case 1:
			return ErrPreempted
		case 2:
			return &WaitError{After: 3 * time.Second, Reason: WaitCircuitOpen}
		}
		return nil
	})
	if err != nil || calls.Load() != 3 {
		t.Fatalf("%v after %d calls", err, calls.Load())
	}
	if got := c.slept(); len(got) != 2 || got[0] != PreemptRetry || got[1] != 3*time.Second {
		t.Fatalf("waits %v", got)
	}
	boom := errors.New("boom")
	if err := w.Do(t.Context(), failing(boom)); !errors.Is(err, boom) {
		t.Fatalf("%v", err)
	}
	if err := w.Do(t.Context(), failing(ErrShuttingDown)); !errors.Is(err, ErrShuttingDown) {
		t.Fatalf("%v", err)
	}
	// A failed request leaves the window as it is and later requests are still sent.
	if err := w.Do(t.Context(), okRequest); err != nil {
		t.Fatal(err)
	}
}

func TestR_REPO_054_AWindowOfOneSendsOneRequestAtATime(t *testing.T) {
	w := newTestWindow(newClock(), 1)
	var inflight, peak atomic.Int32
	done := make(chan error, 6)
	for range 6 {
		go func() {
			done <- w.Do(t.Context(), func(context.Context) error {
				n := inflight.Add(1)
				for {
					p := peak.Load()
					if n <= p || peak.CompareAndSwap(p, n) {
						break
					}
				}
				time.Sleep(2 * time.Millisecond)
				inflight.Add(-1)
				return nil
			})
		}()
	}
	for range 6 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if peak.Load() != 1 {
		t.Fatalf("peak in flight %d", peak.Load())
	}
}

func TestR_REPO_054_ACallWaitingForTheWindowEndsWithItsContext(t *testing.T) {
	w := newTestWindow(newClock(), 1)
	hold, release := make(chan struct{}), make(chan struct{})
	go func() {
		_ = w.Do(context.Background(), func(context.Context) error {
			close(hold)
			<-release
			return nil
		})
	}()
	<-hold
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := w.Do(ctx, okRequest); !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
	close(release)
}
