package httpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// chargeGuard has a budget of 80 MiB (limit 100 MiB), downloads of a large
// object take 1 MiB and the overhead beside a download's buffer is 128 KiB.
func chargeGuard(held *int64) *MemoryGuard {
	g := NewMemoryGuard(MemoryGuardConfig{
		Limit: 100 << 20, RequestBytes: 1 << 20, Overhead: 128 << 10,
		Held: func() int64 { return *held },
		Read: func() (uint64, uint64) { return 0, 0 },
	})
	g.Sample()
	return g
}

func TestMemoryCharges(t *testing.T) {
	t.Run("R-ARCH-005 Q-ARCH-328 a request is charged a download of a large object at admission, until it settles", func(t *testing.T) {
		var held int64
		g := chargeGuard(&held)
		n := 0
		for {
			_, _, reason := g.Admit()
			if reason != "" {
				if reason != ShedMemorySlots {
					t.Fatalf("reason %q", reason)
				}
				break
			}
			n++
		}
		if n != 80 || g.Slots() != 80 || g.InUse() != 80 {
			t.Fatalf("%d admitted, slots %d, in use %d, want 80 each (80 MiB / 1 MiB)", n, g.Slots(), g.InUse())
		}
	})

	t.Run("R-ARCH-005 Q-ARCH-328 a download that settles holds its buffer, window and overhead, so a burst of small downloads that the worst case would have shed is admitted", func(t *testing.T) {
		var held int64
		g := chargeGuard(&held)
		small := int64(64<<10 + 50<<10) // a 50 KiB tarball: its buffer class and its bytes
		n := 0
		for range 1000 {
			tk, _, reason := g.Admit()
			if reason != "" {
				break
			}
			tk.Settle(small)
			n++
		}
		// Each settled download holds 128 KiB + 114 KiB = 242 KiB: 335 of them fit beside the
		// 1 MiB the door needs, where the worst case admitted 80.
		if want := int(((80<<20)-(1<<20))/(128<<10+small)) + 1; n != want || n < 4*80 {
			t.Fatalf("%d settled downloads admitted, want %d (the worst case admitted 80)", n, want)
		}
		if want := int64(n) * (128<<10 + small); g.used.Load() != want {
			t.Fatalf("%d bytes charged, want %d", g.used.Load(), want)
		}
		if _, _, reason := g.Admit(); reason != ShedMemorySlots {
			t.Fatalf("admitted beyond the budget: %q", reason)
		}
	})

	t.Run("R-ARCH-005 Q-ARCH-328 a settled charge never exceeds the charge at admission, and the full-size download keeps its slot", func(t *testing.T) {
		var held int64
		g := chargeGuard(&held)
		tk, release, _ := g.Admit()
		tk.Settle(1 << 30) // more than a download can hold: the charge stays at the slot
		if g.used.Load() != 1<<20 {
			t.Fatalf("%d bytes charged, want the slot's 1 MiB", g.used.Load())
		}
		release()
		if g.used.Load() != 0 {
			t.Fatalf("%d bytes charged after the release", g.used.Load())
		}
	})

	t.Run("R-ARCH-005 Q-ARCH-328 the bytes the caches hold reduce what requests may hold; empty caches reserve nothing", func(t *testing.T) {
		held := int64(0)
		g := chargeGuard(&held)
		if g.Slots() != 80 {
			t.Fatalf("empty caches: %d slots, want 80", g.Slots())
		}
		held = 70 << 20
		g.Sample()
		if g.Slots() != 10 {
			t.Fatalf("70 MiB in the caches: %d slots, want 10", g.Slots())
		}
		n := 0
		for range 80 {
			if _, _, r := g.Admit(); r != "" {
				break
			}
			n++
		}
		if n != 10 {
			t.Fatalf("%d requests admitted beside 70 MiB of caches, want 10", n)
		}
	})

	t.Run("R-ARCH-005 Q-ARCH-328 a second object opened by one request is charged on top, and release gives back everything", func(t *testing.T) {
		var held int64
		g := chargeGuard(&held)
		tk, release, _ := g.Admit()
		tk.Settle(100 << 10)
		tk.Settle(50 << 10)
		if want := int64(128<<10 + 100<<10 + 50<<10); g.used.Load() != want {
			t.Fatalf("%d bytes charged, want %d", g.used.Load(), want)
		}
		release()
		if g.used.Load() != 0 {
			t.Fatalf("%d bytes charged after the release", g.used.Load())
		}
		(*Ticket)(nil).Settle(1 << 20) // a request without a guard
	})

	t.Run("R-ARCH-005 Q-ARCH-328 a request that is alone is always served, however small the limit", func(t *testing.T) {
		g := NewMemoryGuard(MemoryGuardConfig{Limit: 1 << 20, RequestBytes: 4 << 20, Read: func() (uint64, uint64) { return 0, 0 }})
		_, release, reason := g.Admit()
		if reason != "" {
			t.Fatalf("the first request was refused: %q", reason)
		}
		if _, _, reason := g.Admit(); reason != ShedMemorySlots {
			t.Fatalf("a second request beside a full one: %q", reason)
		}
		release()
		if _, _, reason := g.Admit(); reason != "" {
			t.Fatalf("after the release: %q", reason)
		}
	})

	t.Run("R-ARCH-005 Q-ARCH-328 SettleMemory settles the request of its context, and does nothing outside a request", func(t *testing.T) {
		var held int64
		g := chargeGuard(&held)
		SettleMemory(t.Context(), 1<<20) // no ticket in the context
		var during int64
		h := Chain{Router: newTestRouter("GET /d", func(w http.ResponseWriter, r *http.Request) {
			SettleMemory(r.Context(), 64<<10)
			during = g.used.Load()
			w.WriteHeader(http.StatusOK)
		}), Memory: g}.Handler()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/d", nil))
		if w.Code != 200 || during != 128<<10+64<<10 {
			t.Fatalf("status %d, %d bytes charged while the download ran, want %d", w.Code, during, 128<<10+64<<10)
		}
		if g.used.Load() != 0 {
			t.Fatalf("%d bytes charged after the request", g.used.Load())
		}
	})
}
