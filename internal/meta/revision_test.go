package meta

import "testing"

func TestRevisionRegistry(t *testing.T) {
	t.Run("the revision-column registry lists registered columns in table order", func(t *testing.T) {
		r := NewRevisionRegistry()
		r.Register("packages", "rev_seq")
		r.Register("cluster_epoch", "epoch")
		got := r.Columns()
		if len(got) != 2 || got[0].Table != "cluster_epoch" || got[1].Column != "rev_seq" {
			t.Fatalf("Columns() = %+v", got)
		}
		if !r.Has("packages", "rev_seq") || r.Has("packages", "epoch") {
			t.Fatal("Has answers wrongly")
		}
	})
	t.Run("the revision-column registry panics on a duplicate registration", func(t *testing.T) {
		r := NewRevisionRegistry()
		r.Register("packages", "rev_seq")
		mustPanic(t, func() { r.Register("packages", "rev_seq") })
	})
	t.Run("R-ARCH-032 registration after Freeze panics", func(t *testing.T) {
		r := NewRevisionRegistry()
		r.Freeze()
		mustPanic(t, func() { r.Register("a", "b") })
	})
}

func TestRunsWorkers(t *testing.T) {
	t.Run("R-ARCH-001 worker and all run singleton work, web and backup do not", func(t *testing.T) {
		for role, want := range map[string]bool{RoleWeb: false, RoleWorker: true, RoleAll: true, RoleBackup: false} {
			if RunsWorkers(role) != want {
				t.Errorf("RunsWorkers(%q) = %v", role, !want)
			}
		}
	})
}

func mustPanic(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatal("expected a panic")
		}
	}()
	fn()
}
