package verify

import (
	"context"
	"sync"
	"time"
)

// limiter spaces downloads so that, summed over all workers, no more than bps bytes per second are read. A nil limiter and
// a rate of 0 do not wait.
type limiter struct {
	mu   sync.Mutex
	bps  int64
	next time.Time
	now  func() time.Time
}

func newLimiter(bps int64) *limiter {
	if bps <= 0 {
		return nil
	}
	return &limiter{bps: bps, now: time.Now}
}

// wait reserves n bytes of the budget and sleeps until the reservation is due.
func (l *limiter) wait(ctx context.Context, n int) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	now := l.now()
	if l.next.Before(now) {
		l.next = now
	}
	due := l.next
	l.next = l.next.Add(time.Duration(int64(n) * int64(time.Second) / l.bps))
	l.mu.Unlock()
	d := due.Sub(now)
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-t.C:
		return nil
	}
}
