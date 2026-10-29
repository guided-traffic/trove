package tables

import (
	"strings"
	"testing"
)

func TestRegistry(t *testing.T) {
	t.Run("R-BAK-004 a registered table is found with its class", func(t *testing.T) {
		r := New()
		r.Register("job", Data, Options{})
		r.Register("replica", Transient, Options{})
		tb, ok := r.Lookup("replica")
		if !ok || tb.Class != Transient || tb.Class.Exported() {
			t.Fatalf("Lookup(replica) = %+v, %v", tb, ok)
		}
		if got := r.All(); len(got) != 2 || got[0].Name != "job" {
			t.Fatalf("All() = %+v", got)
		}
	})
	t.Run("R-BAK-004 duplicate and late registrations panic", func(t *testing.T) {
		r := New()
		r.Register("a", Data, Options{})
		mustPanic(t, func() { r.Register("a", Data, Options{}) })
		r.Freeze()
		mustPanic(t, func() { r.Register("b", Data, Options{}) })
	})
	t.Run("R-BAK-004 Clone is unfrozen and independent", func(t *testing.T) {
		r := New()
		r.Register("a", Data, Options{})
		r.Freeze()
		c := r.Clone()
		c.Register("b", Data, Options{})
		if _, ok := r.Lookup("b"); ok {
			t.Fatal("clone leaked into the original")
		}
	})
}

func TestCheck(t *testing.T) {
	base := func() *Registry {
		r := New()
		r.Register("package_versions", Data, Options{CycleBreak: "lead_item_id"})
		r.Register("items", Data, Options{})
		r.Register("repositories", Data, Options{})
		r.Register("replica", Transient, Options{})
		return r
	}
	fks := []ForeignKey{
		{"items", "repo_id", "repositories"},
		{"items", "version_id", "package_versions"},
		{"package_versions", "lead_item_id", "items"},
	}
	tables := []string{"package_versions", "items", "repositories", "replica"}

	t.Run("R-BAK-004 a registered cycle break lets the cycle pass", func(t *testing.T) {
		if ps := base().Check(tables, fks); len(ps) != 0 {
			t.Fatalf("unexpected problems: %v", ps)
		}
	})
	t.Run("R-BAK-004 an unregistered table is named", func(t *testing.T) {
		ps := base().Check(append(tables, "x_new"), fks)
		if len(ps) != 1 || ps[0].Table != "x_new" {
			t.Fatalf("problems = %v", ps)
		}
	})
	t.Run("R-BAK-004 a cycle without a CycleBreak names its tables", func(t *testing.T) {
		r := New()
		r.Register("package_versions", Data, Options{})
		r.Register("items", Data, Options{})
		r.Register("repositories", Data, Options{})
		ps := r.Check(tables[:3], fks)
		if len(ps) != 1 || !strings.Contains(ps[0].Reason, "items, package_versions") {
			t.Fatalf("problems = %v", ps)
		}
	})
	t.Run("R-BAK-004 a cycle through a transient table is not a cycle of exported tables", func(t *testing.T) {
		r := New()
		r.Register("a", Data, Options{})
		r.Register("b", Transient, Options{})
		ps := r.Check([]string{"a", "b"}, []ForeignKey{{"a", "b_id", "b"}, {"b", "a_id", "a"}})
		if len(ps) != 0 {
			t.Fatalf("problems = %v", ps)
		}
	})
	t.Run("R-BAK-004 a self reference needs a CycleBreak", func(t *testing.T) {
		r := New()
		r.Register("node", Data, Options{})
		ps := r.Check([]string{"node"}, []ForeignKey{{"node", "parent_id", "node"}})
		if len(ps) != 1 {
			t.Fatalf("problems = %v", ps)
		}
		r2 := New()
		r2.Register("node", Data, Options{CycleBreak: "parent_id"})
		if ps := r2.Check([]string{"node"}, []ForeignKey{{"node", "parent_id", "node"}}); len(ps) != 0 {
			t.Fatalf("problems = %v", ps)
		}
	})
	t.Run("R-BAK-004 a CycleBreak on a column that is no foreign key is refused", func(t *testing.T) {
		r := New()
		r.Register("a", Data, Options{CycleBreak: "nope"})
		if ps := r.Check([]string{"a"}, nil); len(ps) != 1 {
			t.Fatalf("problems = %v", ps)
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
