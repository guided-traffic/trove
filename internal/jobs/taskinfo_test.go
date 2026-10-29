package jobs

import (
	"context"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/config"
)

func nopKind(r *Registry, name string) {
	r.RegisterKind(name, KindSpec{Handler: func(context.Context, *Run) error { return nil }})
}

func TestResolveTasks(t *testing.T) {
	r := NewRegistry()
	nopKind(r, "a")
	nopKind(r, "b")
	nopKind(r, "manual")
	r.RegisterTask("a", TaskSpec{Schedule: "*/5 * * * *"})
	r.RegisterTask("b", TaskSpec{Schedule: "0 2 * * 0", Disabled: true, DedupKey: "shared"})
	r.RegisterTask("manual", TaskSpec{ManualOnly: true})

	t.Run("R-OPS-019 the configuration overrides schedule and enabled", func(t *testing.T) {
		got, err := r.ResolveTasks(config.Maintenance{Tasks: map[string]config.MaintenanceTask{
			"a": {Enabled: true, Schedule: "1 * * * *"}, "b": {Enabled: false, Schedule: "0 3 * * 0"},
		}})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 3 || got[0].Name != "a" || got[0].Schedule != "1 * * * *" || !got[0].Enabled || got[0].Sched == nil {
			t.Fatalf("%+v", got)
		}
		if got[1].Enabled || got[1].DedupKey != "shared" || got[1].Sched != nil {
			t.Fatalf("a disabled task is listed but not parsed: %+v", got[1])
		}
	})
	t.Run("R-OPS-019 a manual-only task has no schedule and no next run", func(t *testing.T) {
		got, err := r.ResolveTasks(config.Maintenance{})
		if err != nil {
			t.Fatal(err)
		}
		m := got[2]
		if m.Name != "manual" || !m.ManualOnly || m.Schedule != "" || m.Sched != nil || m.DedupKey != "manual" {
			t.Fatalf("%+v", m)
		}
	})
	t.Run("R-OPS-019 a maintenance.tasks entry for a manual-only task names the key", func(t *testing.T) {
		_, err := r.ResolveTasks(config.Maintenance{Tasks: map[string]config.MaintenanceTask{"manual": {Enabled: true, Schedule: "* * * * *"}}})
		if err == nil || !strings.Contains(err.Error(), "maintenance.tasks.manual") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("R-OPS-019 a manual-only task cannot carry a schedule", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Fatal("registered")
			}
		}()
		NewRegistry().RegisterTask("x", TaskSpec{ManualOnly: true, Schedule: "* * * * *"})
	})
}
