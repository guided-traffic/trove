package mainclient

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/metrics"
)

// BudgetOptions are the keys of R-BAS-025.
type BudgetOptions struct {
	// MaxConcurrent is bastion.main.max_concurrent: the slots across all replicas.
	MaxConcurrent int
	// Replicas is bastion.replicas, the divisor (Q-BAS-34).
	Replicas int
	// AnonymousShare is bastion.main.anonymous_share, 0 to 1.
	AnonymousShare float64
	// QueueTimeout is bastion.main.queue_timeout.
	QueueTimeout time.Duration
	// Metrics receives the gauges and the exhausted counter; nil counts nothing.
	Metrics *metrics.Metrics
}

// SlotBudget is the concurrency budget of one replica towards the main installation (R-BAS-025, Q-BAS-35): at
// most ceil(max_concurrent / replicas) requests in flight, of which requests on behalf of anonymous callers may
// hold at most the anonymous share. A request that finds no slot waits in arrival order for the queue timeout
// and then gets [ErrBusy]. It is safe for concurrent use and implements [Budget].
type SlotBudget struct {
	total, anon int
	timeout     time.Duration
	m           *metrics.Metrics

	mu      sync.Mutex
	inUse   int
	anonUse int
	queue   []*waiter
}

var _ Budget = (*SlotBudget)(nil)

type waiter struct {
	class   Class
	granted bool
	ch      chan struct{}
}

// NewBudget returns the budget of one replica and exports its size as trove_bastion_main_budget.
func NewBudget(o BudgetOptions) *SlotBudget {
	replicas := max(o.Replicas, 1)
	total := max(int(math.Ceil(float64(max(o.MaxConcurrent, 1))/float64(replicas))), 1)
	// The anonymous cap is the share of the total, rounded down, and at least one slot while the share is
	// above zero, so that a small budget does not lock anonymous callers out altogether (Q-BAS-75).
	anon := 0
	if o.AnonymousShare > 0 {
		anon = min(max(int(math.Floor(float64(total)*o.AnonymousShare+1e-9)), 1), total)
	}
	b := &SlotBudget{total: total, anon: anon, timeout: o.QueueTimeout, m: o.Metrics}
	if b.m != nil {
		b.m.MainBudget.WithLabelValues(metrics.BudgetAuthenticated).Set(float64(total))
		b.m.MainBudget.WithLabelValues(metrics.BudgetAnonymous).Set(float64(anon))
	}
	return b
}

// Slots returns the slots of this replica: in all, and for anonymous callers.
func (b *SlotBudget) Slots() (total, anonymous int) { return b.total, b.anon }

// Acquire implements [Budget].
func (b *SlotBudget) Acquire(ctx context.Context, class Class) (func(), error) {
	b.mu.Lock()
	// After every grant no waiter fits, so a newcomer that fits takes a slot without overtaking anyone.
	if b.fits(class) {
		b.take(class)
		b.mu.Unlock()
		return b.releaser(class), nil
	}
	if class == Anonymous && b.anon == 0 {
		b.mu.Unlock()
		b.exhausted(class)
		return nil, ErrBusy
	}
	w := &waiter{class: class, ch: make(chan struct{})}
	b.queue = append(b.queue, w)
	b.mu.Unlock()

	timer := time.NewTimer(b.timeout)
	defer timer.Stop()
	select {
	case <-w.ch:
		return b.releaser(class), nil
	case <-timer.C:
	case <-ctx.Done():
		if !b.abandon(w) {
			// The slot was granted in the same instant: give it back.
			b.releaser(class)()
		}
		return nil, ctx.Err()
	}
	if b.abandon(w) {
		b.exhausted(class)
		return nil, ErrBusy
	}
	return b.releaser(class), nil
}

// abandon removes a waiter that was not granted and reports whether it was still waiting.
func (b *SlotBudget) abandon(w *waiter) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if w.granted {
		return false
	}
	for i, q := range b.queue {
		if q == w {
			b.queue = append(b.queue[:i], b.queue[i+1:]...)
			break
		}
	}
	return true
}

func (b *SlotBudget) exhausted(class Class) {
	if b.m != nil {
		b.m.BudgetExhausted.WithLabelValues(string(class)).Inc()
	}
}

// fits reports whether a request of the class may take a slot now; b.mu is held.
func (b *SlotBudget) fits(class Class) bool {
	if b.inUse >= b.total {
		return false
	}
	return class != Anonymous || b.anonUse < b.anon
}

// take counts a slot; b.mu is held.
func (b *SlotBudget) take(class Class) {
	b.inUse++
	if class == Anonymous {
		b.anonUse++
	}
	b.gauge()
}

func (b *SlotBudget) gauge() {
	if b.m == nil {
		return
	}
	b.m.MainInFlight.WithLabelValues(metrics.BudgetAuthenticated).Set(float64(b.inUse - b.anonUse))
	b.m.MainInFlight.WithLabelValues(metrics.BudgetAnonymous).Set(float64(b.anonUse))
}

// releaser returns the function that gives the slot back exactly once and hands it to the next waiter that fits.
func (b *SlotBudget) releaser(class Class) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			b.inUse--
			if class == Anonymous {
				b.anonUse--
			}
			b.grant()
			b.gauge()
		})
	}
}

// grant hands free slots to waiters in arrival order; a waiter whose class does not fit is skipped, so an
// anonymous request at its cap never blocks an authenticated one behind it. b.mu is held.
func (b *SlotBudget) grant() {
	kept := b.queue[:0]
	for _, w := range b.queue {
		if b.fits(w.class) {
			b.take(w.class)
			w.granted = true
			close(w.ch)
			continue
		}
		kept = append(kept, w)
	}
	for i := len(kept); i < len(b.queue); i++ {
		b.queue[i] = nil
	}
	b.queue = kept
}
