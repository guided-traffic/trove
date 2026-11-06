package httpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMemoryChargeSequence(t *testing.T) {
	t.Run("R-ARCH-005 Q-ARCH-330 sequential objects opened by one request never exceed the admission charge", func(t *testing.T) {
		var held int64
		g := chargeGuard(&held)
		tk, release, _ := g.Admit()
		defer release()
		var peak int64
		for range 100 {
			undo := tk.Settle(100 << 10)
			peak = max(peak, g.used.Load())
			undo()
		}
		if peak > 1<<20 {
			t.Fatalf("the charge reached %d bytes, more than the admission charge of 1 MiB", peak)
		}
		if g.used.Load() > 1<<20 {
			t.Fatalf("%d bytes charged after 100 sequential opens, more than the admission charge of 1 MiB", g.used.Load())
		}
	})
	t.Run("R-ARCH-005 Q-ARCH-330 a settle after the request's release charges nothing", func(t *testing.T) {
		var held int64
		g := chargeGuard(&held)
		tk, release, _ := g.Admit()
		undo := tk.Settle(100 << 10)
		release()
		tk.Settle(100 << 10)
		undo()
		if g.used.Load() != 0 {
			t.Fatalf("%d bytes charged to a released request", g.used.Load())
		}
	})
	t.Run("R-ARCH-005 Q-ARCH-330 a context without the ticket settles nothing of the request", func(t *testing.T) {
		var held int64
		g := chargeGuard(&held)
		var during int64
		h := Chain{Router: newTestRouter("GET /d", func(w http.ResponseWriter, r *http.Request) {
			SettleMemory(WithoutTicket(r.Context()), 64<<10)()
			during = g.used.Load()
		}), Memory: g}.Handler()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/d", nil))
		if during != 1<<20 {
			t.Fatalf("%d bytes charged, want the unsettled admission charge of 1 MiB", during)
		}
	})
}
