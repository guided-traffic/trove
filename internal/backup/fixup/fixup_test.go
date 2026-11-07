package fixup

import (
	"context"
	"testing"

	"github.com/guided-traffic/trove/internal/meta"
)

func noop(context.Context, meta.Tx, Env) error { return nil }

func TestRegistry(t *testing.T) {
	// The process-wide registry is filled once; the test registers names no real owner uses.
	Register(Fixup{Name: "zz-b", Order: OrderStorageUsage, Apply: noop})
	Register(Fixup{Name: "zz-a", Order: OrderStorageUsage, Apply: noop})
	Register(Fixup{Name: "zz-first", Order: OrderJobs - 1, Apply: noop})
	RegisterPurger(CachePurger{Name: "zz-fmt", Purge: func(context.Context, meta.Runner, Env) error { return nil }})

	t.Run("R-BAK-022 fix-ups run by order, then by name", func(t *testing.T) {
		var got []string
		for _, f := range Fixups() {
			if len(f.Name) > 2 && f.Name[:3] == "zz-" {
				got = append(got, f.Name)
			}
		}
		want := []string{"zz-first", "zz-a", "zz-b"}
		if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	})
	t.Run("R-BAK-008 a purger is listed once per format", func(t *testing.T) {
		n := 0
		for _, p := range Purgers() {
			if p.Name == "zz-fmt" {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("%d purgers named zz-fmt", n)
		}
	})
	t.Run("R-BAK-022 a duplicate name and a nil function are programming errors", func(t *testing.T) {
		for _, f := range []func(){
			func() { Register(Fixup{Name: "zz-a", Apply: noop}) },
			func() { Register(Fixup{Name: "zz-nil"}) },
			func() {
				RegisterPurger(CachePurger{Name: "zz-fmt", Purge: func(context.Context, meta.Runner, Env) error { return nil }})
			},
		} {
			func() {
				defer func() {
					if recover() == nil {
						t.Error("no panic")
					}
				}()
				f()
			}()
		}
	})
}
