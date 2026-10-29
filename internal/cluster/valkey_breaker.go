package cluster

import (
	"sync"
	"time"
)

// breakerState is the state of the circuit breaker.
type breakerState int

const (
	breakerClosed   breakerState = iota // calls pass
	breakerOpen                         // calls are refused until openDuration has passed
	breakerHalfOpen                     // one probe call is in flight
)

// outcome is how a call that passed the breaker ended.
type outcome int

const (
	outcomeSuccess outcome = iota
	outcomeFailure
	// outcomeNeutral is a call that says nothing about Valkey: the caller
	// cancelled it, or the reply was an application error.
	outcomeNeutral
)

// breaker is the per-replica circuit breaker of R-HA-057 rule 3: after
// threshold consecutive failed calls it refuses calls for openDuration, then
// lets one probe through; a successful probe closes it.
type breaker struct {
	threshold    int
	openDuration time.Duration
	now          func() time.Time
	onChange     func(open bool)

	mu       sync.Mutex
	state    breakerState
	failures int
	openedAt time.Time
}

func newBreaker(threshold int, openDuration time.Duration, now func() time.Time, onChange func(open bool)) *breaker {
	if now == nil {
		now = time.Now
	}
	if onChange == nil {
		onChange = func(bool) {}
	}
	return &breaker{threshold: threshold, openDuration: openDuration, now: now, onChange: onChange}
}

// allow reports whether a call may run. A true result obliges the caller to
// report the call's outcome with done.
func (b *breaker) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case breakerClosed:
		return true
	case breakerOpen:
		if b.now().Sub(b.openedAt) < b.openDuration {
			return false
		}
		b.state = breakerHalfOpen
		return true
	default: // half-open: the probe is already out
		return false
	}
}

// done reports the outcome of a call that allow let through.
func (b *breaker) done(o outcome) {
	b.mu.Lock()
	changed, open := false, false
	switch o {
	case outcomeSuccess:
		b.failures = 0
		if b.state != breakerClosed {
			b.state, changed, open = breakerClosed, true, false
		}
	case outcomeFailure:
		b.failures++
		if b.state == breakerHalfOpen || (b.state == breakerClosed && b.failures >= b.threshold) {
			b.state, b.openedAt = breakerOpen, b.now()
			changed, open = true, true
		}
	case outcomeNeutral:
		// A cancelled probe must not wedge the breaker half-open.
		if b.state == breakerHalfOpen {
			b.state = breakerOpen
		}
	}
	b.mu.Unlock()
	if changed {
		b.onChange(open)
	}
}

// isOpen reports whether calls are being refused, including the half-open
// state in which only the probe runs.
func (b *breaker) isOpen() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state != breakerClosed
}
