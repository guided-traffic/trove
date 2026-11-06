package httpserver

import (
	"context"
	"math"
	"runtime"
	"runtime/metrics"
	"sync"
	"sync/atomic"
	"time"
)

// Reasons of a request the in-flight stage refused (trove_http_requests_shed_total).
const (
	ShedInFlight    = "in_flight"    // the endpoint's max_in_flight
	ShedMemorySlots = "memory_slots" // the replica's memory charges
	ShedHeap        = "heap"         // the live heap at the high-water mark
)

// High- and low-water marks of the memory guard, shares of the Go memory limit
// (Q-ARCH-240).
const (
	memoryHighWater = 0.80
	memoryLowWater  = 0.70
)

// MemorySampleInterval is how often the guard reads the heap. The live heap
// changes only at a GC cycle, which near the limit comes every few milliseconds.
const MemorySampleInterval = 50 * time.Millisecond

// memoryRecheck is how long a closed gate waits for a GC cycle before it runs
// one: once the load stops nothing allocates, and the live heap of the last
// cycle would keep the gate closed until the runtime's forced GC two minutes on.
const memoryRecheck = time.Second

// MemoryGuard bounds what the client listeners of one replica admit by its
// memory (R-ARCH-005 step 4, Q-ARCH-240, Q-ARCH-328). Every endpoint's chain
// shares it.
//
//   - Charges: a request is charged what it may hold, a download of a large
//     object (RequestBytes), when it is admitted and until it ends. A download
//     that knows its size hands back the difference at once (Ticket.Settle), so
//     what a long transfer holds is what it uses, and the readers it opens one
//     after the other never pass the admission charge. A request is refused when the
//     charges of the admitted requests plus the bytes the caches hold would pass
//     the high-water mark of the Go memory limit. It bounds a burst that arrives
//     faster than a GC cycle can show it. A charge grows after the admission only
//     for readers open together, beyond the one the admission paid for.
//   - Heap: while the live heap and the goroutine stacks are at the high-water
//     mark, no request is admitted, until they fall below the low-water mark. It
//     bounds what the charges do not see, such as a cache that grew after its
//     requests were admitted.
type MemoryGuard struct {
	budget    int64 // the high-water mark in bytes
	perSlot   int64 // what a download of a large object holds: the charge at admission
	overhead  int64 // what a download holds besides its buffer and window
	high, low uint64
	used      atomic.Int64 // charges of the admitted requests
	held      atomic.Int64 // bytes the caches held at the last sample
	shedding  atomic.Bool
	read      func() (heap, cycles uint64)
	readHeld  func() int64
	onSlots   func(slots int)
	slots     int
	onChange  func(shedding bool, heap uint64)
	collect   func()
	now       func() time.Time

	// The sampling goroutine's own state.
	cycles    uint64
	lastCycle time.Time
}

// MemoryGuardConfig configures NewMemoryGuard.
type MemoryGuardConfig struct {
	// Limit is the Go memory limit in bytes (debug.SetMemoryLimit).
	Limit int64
	// RequestBytes is what a download of a large object holds: the charge at
	// admission and the unit in which Slots and InUse count.
	RequestBytes int64
	// Overhead is what a download holds besides its buffer and receive window:
	// the connection, the goroutine stacks, the request. It is what a settled
	// request keeps charged on top of its download. Zero, or more than
	// RequestBytes, settles to RequestBytes.
	Overhead int64
	// Held returns the bytes the replica's caches hold now; nil holds none. It
	// is read at each sample, not per request.
	Held func() int64
	// Read returns the live heap plus the goroutine stacks and the number of
	// completed GC cycles; nil reads them from runtime/metrics.
	Read func() (heap, cycles uint64)
	// OnChange is called when the heap mark is crossed, with the heap read.
	OnChange func(shedding bool, heap uint64)
	// OnSlots is called when Slots changes, which the caches' bytes cause.
	OnSlots func(slots int)
}

// NewMemoryGuard returns the guard for a limit; nil when no limit is known
// (Limit <= 0 or math.MaxInt64), which admits everything.
func NewMemoryGuard(c MemoryGuardConfig) *MemoryGuard {
	if c.Limit <= 0 || c.Limit == math.MaxInt64 || c.RequestBytes <= 0 {
		return nil
	}
	high := memoryHighWater * float64(c.Limit)
	g := &MemoryGuard{
		budget: int64(high), perSlot: c.RequestBytes, overhead: c.Overhead,
		high: uint64(high), low: uint64(memoryLowWater * float64(c.Limit)),
		read: c.Read, readHeld: c.Held, onSlots: c.OnSlots, onChange: c.OnChange, collect: runtime.GC, now: time.Now,
	}
	if g.overhead <= 0 || g.overhead > g.perSlot {
		g.overhead = g.perSlot
	}
	if g.read == nil {
		g.read = readHeap
	}
	return g
}

// avail is what the admitted requests may hold together: the high-water mark
// less what the caches held at the last sample.
func (g *MemoryGuard) avail() int64 { return g.budget - g.held.Load() }

// Slots is the number of downloads of a large object the guard admits at once
// beside what the caches hold, at least 1; 0 for a nil guard.
func (g *MemoryGuard) Slots() int {
	if g == nil {
		return 0
	}
	return int(max(1, g.avail()/g.perSlot))
}

// InUse is what the admitted requests hold, in downloads of a large object,
// rounded up; 0 for a nil guard.
func (g *MemoryGuard) InUse() int {
	if g == nil {
		return 0
	}
	return int((g.used.Load() + g.perSlot - 1) / g.perSlot)
}

// charge adds n to what the admitted requests hold if it fits. A request is
// always admitted when nothing else is held, so a limit smaller than one
// request still serves one at a time.
func (g *MemoryGuard) charge(n int64) bool {
	for {
		u := g.used.Load()
		if u+n > g.avail() && u > 0 {
			return false
		}
		if g.used.CompareAndSwap(u, u+n) {
			return true
		}
	}
}

// Ticket is what one admitted request holds of the guard.
type Ticket struct {
	g        *MemoryGuard
	mu       sync.Mutex
	held     int64 // what the request is charged now
	open     int64 // the costs of the readers open now
	readers  int
	settled  bool
	released bool
}

// resize moves the charge to what the request holds now: the admission charge
// until the first Settle, then the overhead plus the readers open right now,
// which for one reader at a time never passes the admission charge. mu is held.
func (t *Ticket) resize() {
	if !t.settled {
		return
	}
	want := t.g.overhead + t.open
	if t.readers <= 1 {
		want = min(want, t.g.perSlot)
	}
	t.g.used.Add(want - t.held)
	t.held = want
}

// Settle tells the guard that the request opened a reader whose buffer and
// receive window take n bytes (blob.DownloadCost): the request is charged that
// and the overhead instead of a download of a large object. The returned
// function, called when the reader is closed, takes the n bytes off again, so
// objects read one after the other never pass the admission charge and readers
// open together are charged together. Nothing is charged to a request that has
// ended. A nil ticket charges nothing.
func (t *Ticket) Settle(n int64) (undo func()) {
	if t == nil || n < 0 {
		return func() {}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.released {
		return func() {}
	}
	t.settled = true
	t.open += n
	t.readers++
	t.resize()
	var once sync.Once
	return func() {
		once.Do(func() {
			t.mu.Lock()
			defer t.mu.Unlock()
			if t.released {
				return
			}
			t.open -= n
			t.readers--
			t.resize()
		})
	}
}

// release gives back everything the request holds, and later Settles charge
// nothing.
func (t *Ticket) release() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.g.used.Add(-t.held)
	t.held, t.released = 0, true
}

type ticketKey struct{}

// SettleMemory is Ticket.Settle for the request of ctx; it does nothing for a
// context outside a request, one without a ticket or on a replica without a
// guard.
func SettleMemory(ctx context.Context, n int64) (undo func()) {
	t, _ := ctx.Value(ticketKey{}).(*Ticket)
	return t.Settle(n)
}

// WithoutTicket returns ctx without the request's ticket: work that outlives
// the request, such as a fetch that a flight runs for other waiters, holds its
// own memory and settles nothing of the request's.
func WithoutTicket(ctx context.Context) context.Context {
	return context.WithValue(ctx, ticketKey{}, (*Ticket)(nil))
}

// Admit charges a request what a download of a large object holds. reason is
// empty when the request is admitted; release gives back everything it holds
// and must be called once. The ticket is nil for a nil guard.
func (g *MemoryGuard) Admit() (t *Ticket, release func(), reason string) {
	if g == nil {
		return nil, func() {}, ""
	}
	if g.shedding.Load() {
		return nil, nil, ShedHeap
	}
	if !g.charge(g.perSlot) {
		return nil, nil, ShedMemorySlots
	}
	t = &Ticket{g: g, held: g.perSlot}
	return t, t.release, ""
}

// Run reads the heap every interval until ctx ends.
func (g *MemoryGuard) Run(ctx context.Context, every time.Duration) {
	if g == nil {
		return
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		g.Sample()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Sample reads the caches' bytes and the heap once and moves the gate: closed at
// the high-water mark, open again below the low-water mark. A gate that has
// been closed for memoryRecheck without a GC cycle runs one, so it opens once
// the load is gone. Only one goroutine samples: Run, or a test.
func (g *MemoryGuard) Sample() {
	if g.readHeld != nil {
		g.held.Store(g.readHeld())
		if n := g.Slots(); n != g.slots && g.onSlots != nil {
			g.slots = n
			g.onSlots(n)
		}
	}
	heap, cycles := g.read()
	if t := g.now(); cycles != g.cycles || g.lastCycle.IsZero() {
		g.cycles, g.lastCycle = cycles, t
	} else if g.shedding.Load() && t.Sub(g.lastCycle) >= memoryRecheck {
		g.collect()
		g.lastCycle = t
		heap, g.cycles = g.read()
	}
	was := g.shedding.Load()
	now := was
	switch {
	case !was && heap >= g.high:
		now = true
	case was && heap < g.low:
		now = false
	}
	if now != was {
		g.shedding.Store(now)
		if g.onChange != nil {
			g.onChange(now, heap)
		}
	}
}

// readHeap is the heap the last GC cycle marked live plus the goroutine stacks,
// and the number of completed GC cycles.
func readHeap() (heap, cycles uint64) {
	s := []metrics.Sample{{Name: "/gc/heap/live:bytes"}, {Name: "/memory/classes/heap/stacks:bytes"}, {Name: "/gc/cycles/total:gc-cycles"}}
	metrics.Read(s)
	u := func(i int) uint64 {
		if s[i].Value.Kind() == metrics.KindUint64 {
			return s[i].Value.Uint64()
		}
		return 0
	}
	return u(0) + u(1), u(2)
}
