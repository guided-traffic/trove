package mirror

import (
	"context"
	"errors"
	"sync"
	"time"
)

// errHalted is returned by acquire after the pass stopped taking new requests (a long wait, a stop, a bound, an error).
var errHalted = errors.New("mirror: the pass sends no further request")

// throttle is the window and the waits of one pass attempt (R-REPO-054, Q-REPO-138). The window w starts at 1 in every
// attempt, doubles after w successful requests in a row until it reaches the ceiling or a rate-limit answer arrives (slow
// start), grows by one after w successes in a row afterwards, and halves, at least to 1, once per wait. While a wait lasts
// nothing is sent: acquire blocks, the requests in flight finish, and an answer of one of them that began before the wait
// does not halve the window again nor start another wait.
type throttle struct {
	clock  Clock
	jitter Jitter
	// onWindow is told the window after every change (trove_mirror_concurrency).
	onWindow func(int)

	mu          sync.Mutex
	ceiling     int
	window      int
	slowStart   bool
	inflight    int
	successes   int
	gen         uint64 // the number of waits started so far
	until       time.Time
	consecutive int // rate-limit waits in a row, reset by a success
	halted      bool
	wake        chan struct{}
}

// ticket is the right to send one request, with the number of the wait that was in force when it was given.
type ticket struct{ gen uint64 }

func newThrottle(c Clock, j Jitter, ceiling int, onWindow func(int)) *throttle {
	t := &throttle{clock: c, jitter: j, ceiling: max(1, ceiling), window: 1, slowStart: true, wake: make(chan struct{}), onWindow: onWindow}
	if onWindow != nil {
		onWindow(1)
	}
	return t
}

// Window returns the current window.
func (t *throttle) Window() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.window
}

// acquire blocks until a request may be sent: no wait is in force and fewer than the window are in flight. It returns the
// context's error when ctx ends and errHalted after halt.
func (t *throttle) acquire(ctx context.Context) (ticket, error) {
	for {
		t.mu.Lock()
		if t.halted {
			t.mu.Unlock()
			return ticket{}, errHalted
		}
		if now := t.clock.Now(); now.Before(t.until) {
			d := t.until.Sub(now)
			t.mu.Unlock()
			if err := t.clock.Sleep(ctx, d); err != nil {
				return ticket{}, err
			}
			continue
		}
		if t.inflight < t.window {
			t.inflight++
			tk := ticket{gen: t.gen}
			t.mu.Unlock()
			return tk, nil
		}
		wake := t.wake
		t.mu.Unlock()
		select {
		case <-wake:
		case <-ctx.Done():
			return ticket{}, ctx.Err()
		}
	}
}

// halt makes every acquire, now and later, return errHalted; requests in flight finish.
func (t *throttle) halt() {
	t.mu.Lock()
	t.halted = true
	t.signal()
	t.mu.Unlock()
}

// signal wakes the waiters of acquire; the caller holds the lock.
func (t *throttle) signal() {
	close(t.wake)
	t.wake = make(chan struct{})
}

// setWindow changes the window and tells the observer; the caller holds the lock.
func (t *throttle) setWindow(w int) {
	if w == t.window {
		return
	}
	t.window = w
	if t.onWindow != nil {
		t.onWindow(w)
	}
}

// success ends a request that the upstream answered: it counts towards the growth of the window and resets the run of
// rate-limit answers.
func (t *throttle) success(tk ticket) {
	_ = tk
	t.mu.Lock()
	defer t.mu.Unlock()
	t.inflight--
	t.consecutive = 0
	t.successes++
	if t.successes >= t.window && t.window < t.ceiling {
		t.successes = 0
		if t.slowStart {
			t.setWindow(min(t.ceiling, t.window*2))
		} else {
			t.setWindow(min(t.ceiling, t.window+1))
		}
	}
	t.signal()
}

// failed ends a request that failed for another reason than a rate limit: it interrupts the run of successes and leaves the
// window as it is.
func (t *throttle) failed(tk ticket) {
	_ = tk
	t.mu.Lock()
	defer t.mu.Unlock()
	t.inflight--
	t.successes = 0
	t.signal()
}

// abandoned ends a request that neither succeeded nor failed (pre-empted, cancelled).
func (t *throttle) abandoned(tk ticket) {
	_ = tk
	t.mu.Lock()
	defer t.mu.Unlock()
	t.inflight--
	t.signal()
}

// rateLimited ends a request that the upstream answered with a rate limit. started is true when the answer begins a wait:
// the window halves and wait is how long nothing is sent; false for the answer of a request that was in flight when the
// wait began, which changes nothing. hint is the wait the upstream asked for when hinted.
func (t *throttle) rateLimited(tk ticket, hint time.Duration, hinted bool) (wait time.Duration, started bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.inflight--
	defer t.signal()
	if tk.gen != t.gen {
		return 0, false
	}
	t.gen++
	t.slowStart = false
	t.successes = 0
	t.consecutive++
	t.setWindow(max(1, t.window/2))
	if hinted {
		wait = min(hint, MaxRateLimitWait)
	} else {
		wait = RateLimitBackoff(t.consecutive, t.jitter.Float64())
	}
	// A wait of nothing would send the request again at once and loop on an upstream that keeps asking for it.
	wait = max(wait, time.Second)
	t.until = t.clock.Now().Add(wait)
	return wait, true
}
