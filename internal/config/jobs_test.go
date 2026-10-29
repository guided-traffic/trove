package config

import "testing"

func TestJobsWorkersQueues(t *testing.T) {
	w := JobsWorkers{Default: 4, Maintenance: 1, Index: 2, Delivery: 8}
	t.Run("R-ARCH-016 every queue has its worker slots", func(t *testing.T) {
		want := map[string]int{"default": 4, "maintenance": 1, "index": 2, "delivery": 8}
		for _, q := range w.Queues() {
			n, ok := w.Count(q)
			if !ok || n != want[q] {
				t.Errorf("Count(%s) = %d, %v", q, n, ok)
			}
		}
		if len(w.Queues()) != len(want) {
			t.Errorf("Queues = %v", w.Queues())
		}
		if _, ok := w.Count("elsewhere"); ok {
			t.Error("an unknown queue was accepted")
		}
	})
}
