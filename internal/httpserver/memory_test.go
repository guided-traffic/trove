package httpserver

import (
	"math"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMemoryGuard(t *testing.T) {
	t.Run("R-ARCH-005 the memory slots are high-water times the Go memory limit over the request bytes, and no known limit means no guard", func(t *testing.T) {
		g := NewMemoryGuard(MemoryGuardConfig{Limit: 100 << 20, RequestBytes: 1 << 20, Read: func() (uint64, uint64) { return 0, 0 }})
		if g.Slots() != 80 {
			t.Fatalf("slots = %d, want 80", g.Slots())
		}
		if g := NewMemoryGuard(MemoryGuardConfig{Limit: 100 << 20, RequestBytes: 1 << 20, Reserved: 30 << 20}); g.Slots() != 50 {
			t.Fatalf("with 30 MiB of cache budgets the slots are %d, want (80 - 30) MiB / 1 MiB = 50 (Q-ARCH-243)", g.Slots())
		}
		if g := NewMemoryGuard(MemoryGuardConfig{Limit: 100 << 20, RequestBytes: 1 << 20, Reserved: 90 << 20}); g.Slots() != 1 {
			t.Fatalf("cache budgets above the high-water mark give %d slots, want 1", g.Slots())
		}
		if g := NewMemoryGuard(MemoryGuardConfig{Limit: 1 << 20, RequestBytes: 4 << 20}); g.Slots() != 1 {
			t.Fatalf("a limit below one request gives %d slots, want 1", g.Slots())
		}
		for _, l := range []int64{0, math.MaxInt64} {
			g := NewMemoryGuard(MemoryGuardConfig{Limit: l, RequestBytes: 1 << 20})
			if g != nil || g.Slots() != 0 {
				t.Fatalf("limit %d: a guard without a known limit", l)
			}
			release, reason := g.Admit()
			if reason != "" {
				t.Fatalf("limit %d: a nil guard refused with %q", l, reason)
			}
			release()
			g.Run(t.Context(), MemorySampleInterval) // returns at once
		}
	})

	t.Run("R-ARCH-005 InUse counts the slots admitted requests hold, and a full guard sheds", func(t *testing.T) {
		g := NewMemoryGuard(MemoryGuardConfig{Limit: 5 << 20, RequestBytes: 2 << 20, Read: func() (uint64, uint64) { return 0, 0 }})
		r1, _ := g.Admit()
		r2, _ := g.Admit()
		if g.Slots() != 2 || g.InUse() != 2 {
			t.Fatalf("slots %d, in use %d, want 2 and 2", g.Slots(), g.InUse())
		}
		if _, reason := g.Admit(); reason != ShedMemorySlots {
			t.Fatalf("a full guard answered %q, want %q", reason, ShedMemorySlots)
		}
		r1()
		if g.InUse() != 1 {
			t.Fatalf("in use %d after one release, want 1", g.InUse())
		}
		r2()
		if (*MemoryGuard)(nil).InUse() != 0 {
			t.Fatal("a nil guard reports slots in use")
		}
	})

	t.Run("R-ARCH-005 the heap gate closes at the high-water mark, stays closed above the low-water mark and opens below it", func(t *testing.T) {
		var heap atomic.Uint64
		var changes []bool
		cycles := uint64(0)
		g := NewMemoryGuard(MemoryGuardConfig{
			Limit: 1000, RequestBytes: 1, Read: func() (uint64, uint64) { cycles++; return heap.Load(), cycles },
			OnChange: func(s bool, _ uint64) { changes = append(changes, s) },
		})
		steps := []struct {
			heap   uint64
			reason string
		}{
			{100, ""},
			{799, ""},
			{800, ShedHeap}, // 80 % of the limit
			{750, ShedHeap},
			{700, ShedHeap},
			{699, ""}, // below 70 %
			{790, ""},
		}
		for _, s := range steps {
			heap.Store(s.heap)
			g.Sample()
			release, reason := g.Admit()
			if reason != s.reason {
				t.Fatalf("heap %d: reason %q, want %q", s.heap, reason, s.reason)
			}
			if release != nil {
				release()
			}
		}
		if len(changes) != 2 || !changes[0] || changes[1] {
			t.Fatalf("OnChange calls %v, want [true false]", changes)
		}
	})

	t.Run("R-ARCH-005 the memory slots bound the requests of every endpoint together: beyond them a request gets 503 server_busy with Retry-After 1 and the reason is counted", func(t *testing.T) {
		g := NewMemoryGuard(MemoryGuardConfig{Limit: 10 << 20, RequestBytes: 4 << 20, Read: func() (uint64, uint64) { return 0, 0 }})
		if g.Slots() != 2 {
			t.Fatalf("slots = %d", g.Slots())
		}
		started, release := make(chan struct{}, 2), make(chan struct{})
		slow := func(w http.ResponseWriter, r *http.Request) {
			started <- struct{}{}
			<-release
		}
		var mu sync.Mutex
		shed := map[string]int{}
		chain := func() http.Handler {
			return Chain{Router: newTestRouter("GET /slow", slow), MaxInFlight: 10, Memory: g, OnShed: func(r string) {
				mu.Lock()
				shed[r]++
				mu.Unlock()
			}}.Handler()
		}
		a, b := chain(), chain() // two endpoints of one replica
		var wg sync.WaitGroup
		for _, h := range []http.Handler{a, b} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/slow", nil))
			}()
		}
		<-started
		<-started
		w := httptest.NewRecorder()
		a.ServeHTTP(w, httptest.NewRequest("GET", "/slow", nil))
		if w.Code != 503 || w.Header().Get("Retry-After") != "1" || problemOf(t, w).Code != "server_busy" {
			t.Fatalf("status %d, Retry-After %q, body %s", w.Code, w.Header().Get("Retry-After"), w.Body)
		}
		if shed[ShedMemorySlots] != 1 || len(shed) != 1 {
			t.Fatalf("shed = %v", shed)
		}
		close(release)
		wg.Wait()
		w = httptest.NewRecorder()
		router := newTestRouter("GET /fast", func(http.ResponseWriter, *http.Request) {})
		Chain{Router: router, Memory: g}.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/fast", nil))
		if w.Code != 200 {
			t.Fatalf("slots not released: %d", w.Code)
		}
	})

	t.Run("R-ARCH-005 the endpoint's max_in_flight is asked first and counted as in_flight", func(t *testing.T) {
		g := NewMemoryGuard(MemoryGuardConfig{Limit: 100 << 20, RequestBytes: 1 << 20, Read: func() (uint64, uint64) { return 0, 0 }})
		started, release := make(chan struct{}), make(chan struct{})
		var reasons []string
		h := Chain{Router: newTestRouter("GET /slow", func(http.ResponseWriter, *http.Request) {
			close(started)
			<-release
		}), MaxInFlight: 1, Memory: g, OnShed: func(r string) { reasons = append(reasons, r) }}.Handler()
		done := make(chan struct{})
		go func() {
			defer close(done)
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/slow", nil))
		}()
		<-started
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/slow", nil))
		close(release)
		<-done
		if w.Code != 503 || len(reasons) != 1 || reasons[0] != ShedInFlight {
			t.Fatalf("status %d, reasons %v", w.Code, reasons)
		}
		if len(g.slots) != 0 {
			t.Fatalf("%d memory slots still taken", len(g.slots))
		}
	})

	t.Run("R-ARCH-005 while the heap gate is closed a request is refused with the reason heap", func(t *testing.T) {
		g := NewMemoryGuard(MemoryGuardConfig{Limit: 1000, RequestBytes: 1, Read: func() (uint64, uint64) { return 900, 1 }})
		g.Sample()
		var reasons []string
		w := httptest.NewRecorder()
		Chain{
			Router: newTestRouter("GET /x", func(http.ResponseWriter, *http.Request) { t.Error("the handler ran") }),
			Memory: g, OnShed: func(r string) { reasons = append(reasons, r) },
		}.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/x", nil))
		if w.Code != 503 || w.Header().Get("Retry-After") != "1" || len(reasons) != 1 || reasons[0] != ShedHeap {
			t.Fatalf("status %d, reasons %v", w.Code, reasons)
		}
	})

	t.Run("R-ARCH-005 a gate closed without a GC cycle for a second runs one, so it opens once the load is gone", func(t *testing.T) {
		heap, cycles, collected := uint64(900), uint64(7), 0
		g := NewMemoryGuard(MemoryGuardConfig{Limit: 1000, RequestBytes: 1, Read: func() (uint64, uint64) { return heap, cycles }})
		clock := time.Unix(0, 0)
		g.now = func() time.Time { return clock }
		g.collect = func() { collected++; cycles++; heap = 100 }
		g.Sample()
		if _, r := g.Admit(); r != ShedHeap {
			t.Fatalf("reason %q, want heap", r)
		}
		clock = clock.Add(memoryRecheck / 2)
		g.Sample()
		if collected != 0 {
			t.Fatal("a GC ran before the recheck interval")
		}
		clock = clock.Add(memoryRecheck / 2)
		g.Sample()
		if collected != 1 {
			t.Fatalf("%d GC runs, want 1", collected)
		}
		release, r := g.Admit()
		if r != "" {
			t.Fatalf("the gate stayed closed after the GC showed the heap free: %q", r)
		}
		release()
		clock = clock.Add(10 * memoryRecheck)
		g.Sample()
		if collected != 1 {
			t.Fatal("an open gate ran a GC")
		}
	})

	t.Run("R-ARCH-005 the heap read from runtime/metrics is the live heap plus the stacks, with the GC cycles", func(t *testing.T) {
		runtime.GC()
		if heap, cycles := readHeap(); heap == 0 || cycles == 0 {
			t.Fatalf("runtime/metrics gave heap %d after %d cycles", heap, cycles)
		}
	})
}
