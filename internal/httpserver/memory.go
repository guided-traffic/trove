package httpserver

import (
	"context"
	"math"
	"runtime"
	"runtime/metrics"
	"sync/atomic"
	"time"
)

// Reasons of a request the in-flight stage refused (trove_http_requests_shed_total).
const (
	ShedInFlight    = "in_flight"    // the endpoint's max_in_flight
	ShedMemorySlots = "memory_slots" // the replica's memory slots
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
// memory (R-ARCH-005 step 4, Q-ARCH-240). Every endpoint's chain shares it.
//
//   - Slots: a request holds one of high-water × limit / request bytes slots from
//     the in-flight stage to its end. It bounds a burst that arrives faster than
//     a GC cycle can show it.
//   - Heap: while the live heap and the goroutine stacks are at the high-water
//     mark, no request is admitted, until they fall below the low-water mark. It
//     bounds requests that hold more than a download does.
type MemoryGuard struct {
	slots     chan struct{}
	high, low uint64
	shedding  atomic.Bool
	read      func() (heap, cycles uint64)
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
	// RequestBytes is what one request may hold: the slots are high-water ×
	// Limit / RequestBytes.
	RequestBytes int64
	// Read returns the live heap plus the goroutine stacks and the number of
	// completed GC cycles; nil reads them from runtime/metrics.
	Read func() (heap, cycles uint64)
	// OnChange is called when the heap mark is crossed, with the heap read.
	OnChange func(shedding bool, heap uint64)
}

// NewMemoryGuard returns the guard for a limit; nil when no limit is known
// (Limit <= 0 or math.MaxInt64), which admits everything.
func NewMemoryGuard(c MemoryGuardConfig) *MemoryGuard {
	if c.Limit <= 0 || c.Limit == math.MaxInt64 || c.RequestBytes <= 0 {
		return nil
	}
	high := memoryHighWater * float64(c.Limit)
	g := &MemoryGuard{
		slots: make(chan struct{}, max(1, int(high/float64(c.RequestBytes)))),
		high:  uint64(high), low: uint64(memoryLowWater * float64(c.Limit)),
		read: c.Read, onChange: c.OnChange, collect: runtime.GC, now: time.Now,
	}
	if g.read == nil {
		g.read = readHeap
	}
	return g
}

// Slots is the number of requests the guard admits at once; 0 for a nil guard.
func (g *MemoryGuard) Slots() int {
	if g == nil {
		return 0
	}
	return cap(g.slots)
}

// Admit takes a slot. reason is empty when the request is admitted; release
// gives the slot back and must be called once.
func (g *MemoryGuard) Admit() (release func(), reason string) {
	if g == nil {
		return func() {}, ""
	}
	if g.shedding.Load() {
		return nil, ShedHeap
	}
	select {
	case g.slots <- struct{}{}:
		return func() { <-g.slots }, ""
	default:
		return nil, ShedMemorySlots
	}
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

// Sample reads the heap once and moves the gate: closed at the high-water mark,
// open again below the low-water mark. A gate that has been closed for
// memoryRecheck without a GC cycle runs one, so it opens once the load is gone.
// Only one goroutine samples: Run, or a test.
func (g *MemoryGuard) Sample() {
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
