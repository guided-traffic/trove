package observability

import (
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"
)

func msDuration(ms float64) time.Duration { return time.Duration(ms * float64(time.Millisecond)) }

// TestR_OPS_003_RequestWindowPercentiles: the window of the runtime snapshot (Q-OPS-175) yields the percentiles of the
// requests it holds, interpolated inside their bucket as histogram_quantile does, counts the 5xx, and starts empty
// after each drain.
func TestR_OPS_003_RequestWindowPercentiles(t *testing.T) {
	t.Run("the bucket bounds run from 0.5 ms to 65.536 s a quarter octave apart", func(t *testing.T) {
		if latencyBounds[0] != 0.5 || latencyBounds[latencyFinite-1] != 65536 {
			t.Fatalf("bounds %v .. %v", latencyBounds[0], latencyBounds[latencyFinite-1])
		}
		for i := 1; i < latencyFinite; i++ {
			if r := latencyBounds[i] / latencyBounds[i-1]; math.Abs(r-math.Pow(2, 0.25)) > 1e-9 {
				t.Fatalf("bound %d / %d = %v", i, i-1, r)
			}
		}
		for d, want := range map[time.Duration]int{
			0: 0, msDuration(0.5): 0, msDuration(0.5) + 1: 1, time.Second: latencyBucket(time.Second),
			65536 * time.Millisecond: latencyFinite - 1, 70 * time.Second: latencyFinite,
		} {
			if got := latencyBucket(d); got != want {
				t.Errorf("bucket of %s = %d, want %d", d, got, want)
			}
		}
	})

	t.Run("a percentile is interpolated linearly inside its bucket", func(t *testing.T) {
		w := NewRequestWindow()
		i := latencyBucket(10 * time.Millisecond)
		lower, upper := latencyBounds[i-1], latencyBounds[i]
		for range 4 {
			w.observe(200, 10*time.Millisecond)
		}
		got := w.Drain()
		if got.Requests != 4 || got.Errors != 0 {
			t.Fatalf("counts %+v", got)
		}
		want := math.Round((lower+(upper-lower)*0.5)*1000) / 1000
		if got.P50 == nil || *got.P50 != want {
			t.Errorf("p50 = %v, want %v (bucket %v..%v)", deref(got.P50), want, lower, upper)
		}
		if got.P99 == nil || *got.P99 > upper || *got.P99 <= *got.P50 {
			t.Errorf("p99 = %v, want in (%v, %v]", deref(got.P99), *got.P50, upper)
		}
	})

	t.Run("the percentiles of 1 to 100 ms are within the bucket width of the true values", func(t *testing.T) {
		w := NewRequestWindow()
		for ms := 1; ms <= 100; ms++ {
			w.observe(200, time.Duration(ms)*time.Millisecond)
		}
		got := w.Drain()
		for _, c := range []struct {
			name string
			v    *float64
			want float64
		}{{"p50", got.P50, 50}, {"p95", got.P95, 95}, {"p99", got.P99, 99}} {
			if c.v == nil || math.Abs(*c.v-c.want)/c.want > 0.19 {
				t.Errorf("%s = %v, want %v ± 19 %%", c.name, deref(c.v), c.want)
			}
		}
	})

	t.Run("a request above the last bound reads as the last bound", func(t *testing.T) {
		w := NewRequestWindow()
		w.observe(200, 2*time.Minute)
		if got := w.Drain(); got.P99 == nil || *got.P99 != 65536 {
			t.Errorf("p99 = %v", deref(got.P99))
		}
	})

	t.Run("5xx count as errors, 4xx do not; a drain starts the next window empty", func(t *testing.T) {
		w := NewRequestWindow()
		for _, status := range []int{200, 404, 429, 500, 503, 599} {
			w.observe(status, time.Millisecond)
		}
		if got := w.Drain(); got.Requests != 6 || got.Errors != 3 {
			t.Errorf("first window %+v", got)
		}
		got := w.Drain()
		if got.Requests != 0 || got.Errors != 0 || got.P50 != nil || got.P95 != nil || got.P99 != nil {
			t.Errorf("second window %+v, want empty with no percentiles", got)
		}
	})

	t.Run("a nil window ignores everything", func(t *testing.T) {
		var w *RequestWindow
		w.enter()
		w.observe(500, time.Second)
		w.leave()
		if got := w.Drain(); got != (WindowCounts{}) {
			t.Errorf("%+v", got)
		}
	})
}

// TestR_OPS_003_RequestLogFeedsTheWindow: RequestLog with WithRequestWindow counts a request in flight while it is
// served and then its status and duration, as the metrics see them.
func TestR_OPS_003_RequestLogFeedsTheWindow(t *testing.T) {
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := NewRequestWindow()
	entered, release := make(chan struct{}), make(chan struct{})
	h := RequestLog(quiet, NewMetrics("api"), WithRequestWindow(w))(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
		rw.WriteHeader(http.StatusServiceUnavailable)
	}))
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/system/ping", nil))
	}()
	<-entered
	if got := w.Drain(); got.InFlight != 1 || got.Requests != 0 {
		t.Errorf("while served: %+v", got)
	}
	close(release)
	<-done
	if got := w.Drain(); got.InFlight != 0 || got.Requests != 1 || got.Errors != 1 || got.P50 == nil {
		t.Errorf("after: %+v", got)
	}
}

// TestR_OPS_003_RuntimeSampler: a sample covers the period since the previous one (the first since the sampler was
// built), divides the CPU time used by it, drains the request window, and reports GOMAXPROCS, the memory the runtime
// holds, the given memory limit and the goroutines.
func TestR_OPS_003_RuntimeSampler(t *testing.T) {
	start := time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)
	now, cpu := start, 2*time.Second
	w := NewRequestWindow()
	s := newRuntimeSampler(w, 512<<20, func() time.Time { return now }, func() time.Duration { return cpu })

	w.observe(200, 3*time.Millisecond)
	w.observe(502, 40*time.Millisecond)
	now, cpu = start.Add(10*time.Second), 7*time.Second
	got := s.Sample()
	if got.Window != 10*time.Second || got.CPUCores != 0.5 || !got.SampledAt.Equal(now) {
		t.Errorf("first sample: window %s, cores %v, at %s", got.Window, got.CPUCores, got.SampledAt)
	}
	if got.Requests != 2 || got.Errors != 1 || got.P50 == nil {
		t.Errorf("first sample's requests: %+v", got.WindowCounts)
	}
	if got.GOMAXPROCS != runtime.GOMAXPROCS(0) || got.MemoryLimitBytes != 512<<20 || got.MemoryBytes <= 0 || got.Goroutines < 1 {
		t.Errorf("process: GOMAXPROCS %d, memory %d of %d, goroutines %d", got.GOMAXPROCS, got.MemoryBytes, got.MemoryLimitBytes, got.Goroutines)
	}

	now = now.Add(2 * time.Second)
	got = s.Sample()
	if got.Window != 2*time.Second || got.CPUCores != 0 || got.Requests != 0 || got.P99 != nil {
		t.Errorf("second sample: window %s, cores %v, %+v", got.Window, got.CPUCores, got.WindowCounts)
	}

	cpu -= time.Second // a clock that went back reads as idle, never as negative
	now = now.Add(time.Second)
	if got = s.Sample(); got.CPUCores != 0 {
		t.Errorf("cores %v after the CPU time went back", got.CPUCores)
	}
}

// TestR_OPS_003_ProcessMeasurements: the CPU time of the process grows while it computes, and the memory the runtime
// holds grows with a live allocation; both are read from the process, not from the garbage collector's last cycle.
func TestR_OPS_003_ProcessMeasurements(t *testing.T) {
	before := processCPUTime()
	deadline := time.Now().Add(10 * time.Second)
	x := 1.0
	for processCPUTime()-before < 50*time.Millisecond {
		for i := range 100_000 {
			x = math.Sqrt(x + float64(i))
		}
		if time.Now().After(deadline) {
			t.Fatalf("the CPU time rose by %s in 10 s of computing", processCPUTime()-before)
		}
	}

	mem := runtimeMemoryBytes()
	b := make([]byte, 64<<20)
	for i := 0; i < len(b); i += 4096 {
		b[i] = byte(x)
	}
	if grown := runtimeMemoryBytes() - mem; grown < 48<<20 {
		t.Errorf("a live allocation of 64 MiB grew the memory by %d bytes", grown)
	}
	runtime.KeepAlive(b)
}

func deref(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}
