package observability

import (
	"math"
	"runtime"
	"runtime/metrics"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// The replica's own measurements that its heartbeat writes into the replica
// registry (R-OPS-003, Q-OPS-175): the requests its client listener finished
// since the previous heartbeat, and its CPU, memory and goroutines. Replicas
// never ask each other, and nothing here reads Prometheus: the window is an
// accumulator of its own, filled by RequestLog next to the trove_http_*
// families and emptied by every sample.

// The latency buckets of a window: upper bounds of 0.5 ms × 2^(i/4), a quarter
// octave apart, from 0.5 ms to 65.536 s, and one bucket above. A percentile is
// interpolated linearly inside its bucket, as Prometheus' histogram_quantile
// does, so it is off by at most the bucket's width, 19 % of its upper bound.
const (
	latencyFirstBoundMs = 0.5
	latencyPerOctave    = 4
	latencyFinite       = 69 // the last finite bound is 0.5 ms × 2^17
	latencyBuckets      = latencyFinite + 1
)

// latencyBounds are the finite upper bounds in milliseconds; an array, filled
// once, never changed.
var latencyBounds = func() (b [latencyFinite]float64) {
	for i := range b {
		b[i] = latencyFirstBoundMs * math.Pow(2, float64(i)/latencyPerOctave)
	}
	return b
}()

// latencyBucket is the index of the bucket a duration falls into: the first
// whose upper bound is at least the duration, or the bucket above the last.
func latencyBucket(d time.Duration) int {
	ms := float64(d) / float64(time.Millisecond)
	return sort.SearchFloat64s(latencyBounds[:], ms)
}

// RequestWindow accumulates the requests of the client listener between two
// samples (Q-OPS-175). RequestLog feeds it through WithRequestWindow; one window
// is shared by the chains of every endpoint of the process. A nil window
// ignores everything.
type RequestWindow struct {
	inFlight atomic.Int64

	mu     sync.Mutex
	counts [latencyBuckets]int64
	errors int64
}

// NewRequestWindow returns an empty window.
func NewRequestWindow() *RequestWindow { return &RequestWindow{} }

func (w *RequestWindow) enter() {
	if w != nil {
		w.inFlight.Add(1)
	}
}

func (w *RequestWindow) leave() {
	if w != nil {
		w.inFlight.Add(-1)
	}
}

// observe counts one finished request: its status and its duration, measured
// as RequestLog measures them for trove_http_request_duration_seconds.
func (w *RequestWindow) observe(status int, d time.Duration) {
	if w == nil {
		return
	}
	i := latencyBucket(d)
	w.mu.Lock()
	w.counts[i]++
	if status >= 500 {
		w.errors++
	}
	w.mu.Unlock()
}

// WindowCounts is what a window held when it was drained.
type WindowCounts struct {
	// Requests finished in the window; Errors those of them answered with a
	// 5xx status.
	Requests int64
	Errors   int64
	// InFlight is the requests being served at the drain, which is not reset.
	InFlight int64
	// P50, P95 and P99 are the latency percentiles in milliseconds; nil when no
	// request finished.
	P50, P95, P99 *float64
}

// Drain returns what the window holds and starts the next one empty.
func (w *RequestWindow) Drain() WindowCounts {
	if w == nil {
		return WindowCounts{}
	}
	w.mu.Lock()
	counts, errors := w.counts, w.errors
	w.counts, w.errors = [latencyBuckets]int64{}, 0
	w.mu.Unlock()
	out := WindowCounts{Errors: errors, InFlight: w.inFlight.Load()}
	for _, n := range counts {
		out.Requests += n
	}
	out.P50 = latencyQuantile(0.5, counts, out.Requests)
	out.P95 = latencyQuantile(0.95, counts, out.Requests)
	out.P99 = latencyQuantile(0.99, counts, out.Requests)
	return out
}

// latencyQuantile is the q-quantile of the bucket counts in milliseconds,
// rounded to the microsecond, or nil without a request. A rank in the bucket
// above the last finite bound is that bound, as in histogram_quantile.
func latencyQuantile(q float64, counts [latencyBuckets]int64, total int64) *float64 {
	if total == 0 {
		return nil
	}
	rank := q * float64(total)
	var below int64
	for i, n := range counts {
		if n == 0 || float64(below+n) < rank {
			below += n
			continue
		}
		if i == latencyFinite {
			v := latencyBounds[latencyFinite-1]
			return &v
		}
		lower := 0.0
		if i > 0 {
			lower = latencyBounds[i-1]
		}
		v := lower + (latencyBounds[i]-lower)*(rank-float64(below))/float64(n)
		v = math.Round(v*1000) / 1000
		return &v
	}
	return nil // unreachable: the counts sum to total
}

// RuntimeSample is one measurement of the process over the period since the
// previous sample (Q-OPS-175).
type RuntimeSample struct {
	SampledAt time.Time
	// Window is the period since the previous sample; the first sample covers
	// the time since the sampler was built, at the start of the replica.
	Window time.Duration
	WindowCounts
	// CPUCores is the process's CPU time (user and system) per second of the
	// window: 1.0 is one core busy.
	CPUCores float64
	// GOMAXPROCS is the cores the Go scheduler runs code on, which the runtime
	// derives from the container's CPU limit unless the GOMAXPROCS variable sets it.
	GOMAXPROCS int
	// MemoryBytes is what the Go runtime holds from the system: every class of
	// /memory/classes/total:bytes except the heap it released.
	MemoryBytes int64
	// MemoryLimitBytes is the container limit the sampler was given; 0 when none
	// is known.
	MemoryLimitBytes int64
	Goroutines       int
}

// RuntimeSampler takes the samples of one process. The previous sample's time
// and CPU time are the sampler's own state; the composition root owns one per
// replica.
type RuntimeSampler struct {
	window   *RequestWindow
	memLimit int64
	now      func() time.Time
	cpuTime  func() time.Duration

	mu      sync.Mutex
	prevAt  time.Time
	prevCPU time.Duration
}

// NewRuntimeSampler builds the sampler of the process: window is the client
// listener's window (nil reports no requests), memoryLimitBytes the container's
// memory limit (runtime.memory_limit_bytes, 0 for none). The first sample
// covers the time from here.
func NewRuntimeSampler(window *RequestWindow, memoryLimitBytes int64) *RuntimeSampler {
	return newRuntimeSampler(window, memoryLimitBytes, time.Now, processCPUTime)
}

func newRuntimeSampler(window *RequestWindow, memoryLimitBytes int64, now func() time.Time, cpuTime func() time.Duration) *RuntimeSampler {
	s := &RuntimeSampler{window: window, memLimit: memoryLimitBytes, now: now, cpuTime: cpuTime}
	s.prevAt, s.prevCPU = now(), cpuTime()
	return s
}

// Sample measures the period since the previous sample and starts the next
// one: it drains the request window.
func (s *RuntimeSampler) Sample() RuntimeSample {
	s.mu.Lock()
	defer s.mu.Unlock()
	now, cpu := s.now(), s.cpuTime()
	out := RuntimeSample{
		SampledAt: now, Window: now.Sub(s.prevAt), WindowCounts: s.window.Drain(),
		GOMAXPROCS: runtime.GOMAXPROCS(0), MemoryBytes: runtimeMemoryBytes(),
		MemoryLimitBytes: s.memLimit, Goroutines: runtime.NumGoroutine(),
	}
	if used := cpu - s.prevCPU; out.Window > 0 && used > 0 {
		out.CPUCores = used.Seconds() / out.Window.Seconds()
	}
	s.prevAt, s.prevCPU = now, cpu
	return out
}

// runtimeMemoryBytes is the memory the Go runtime has mapped from the system
// minus the heap it returned (runtime/metrics, read without stopping the world).
func runtimeMemoryBytes() int64 {
	s := []metrics.Sample{{Name: "/memory/classes/total:bytes"}, {Name: "/memory/classes/heap/released:bytes"}}
	metrics.Read(s)
	if s[0].Value.Kind() != metrics.KindUint64 || s[1].Value.Kind() != metrics.KindUint64 {
		return 0
	}
	total, released := s[0].Value.Uint64(), s[1].Value.Uint64()
	if released > total {
		return 0
	}
	return int64(min(total-released, math.MaxInt64)) //nolint:gosec // bounded by MaxInt64 on the line itself
}
