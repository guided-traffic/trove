package app

import (
	"context"
	"testing"
)

type fakeQuiescer struct {
	name string
}

func (f *fakeQuiescer) Name() string                { return f.name }
func (f *fakeQuiescer) Start(context.Context) error { return nil }
func (f *fakeQuiescer) Stop(context.Context) error  { return nil }
func (f *fakeQuiescer) Quiesce(context.Context)     {}

type firstQuiescer struct{ fakeQuiescer }

func (*firstQuiescer) QuiescesFirst() {}

type plainComponent struct{ fakeQuiescer }

// TestQuiesceOrder: the job worker is asked to quiesce before the engines its handlers use, whatever the order the components were
// started in; the others keep the reverse start order (R-ARCH-024 step 4, R-REPO-053, Q-ARCH-318).
func TestQuiesceOrder(t *testing.T) {
	worker := &firstQuiescer{fakeQuiescer{name: "jobs"}}
	engine := &fakeQuiescer{name: "proxy"}
	other := &fakeQuiescer{name: "other"}
	comps := []Component{worker, engine, &plainComponent{}, other} // started in this order: the engine after the worker
	got := quiesceOrder(comps)
	var names []string
	for _, q := range got {
		names = append(names, q.(Component).Name())
	}
	want := []string{"jobs", "other", "", "proxy"}
	if len(names) != len(want) {
		t.Fatalf("%v", names)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("R-ARCH-024 the order is %v, want %v", names, want)
		}
	}
}

// TestTheRegisteredWorkerComponentQuiescesFirst: the component the coordination wiring registers (workerGroup, which holds the job
// worker in a named field) is split into the components that quiesce first, ahead of an engine that started after it; the marker on
// *jobs.Worker alone is not in its method set (R-ARCH-024 step 4, Q-ARCH-318, Q-ARCH-329).
func TestTheRegisteredWorkerComponentQuiescesFirst(t *testing.T) {
	engine := &fakeQuiescer{name: "proxy"}
	first, rest := quiesceGroups([]Component{&workerGroup{}, engine})
	if len(first) != 1 || len(rest) != 1 {
		t.Fatalf("R-ARCH-024 first=%d rest=%d, want the jobs component first and the engine in the rest", len(first), len(rest))
	}
	if c, ok := first[0].(Component); !ok || c.Name() != "jobs" {
		t.Fatalf("R-ARCH-024 the component that quiesces first is %v, want jobs", first[0])
	}
}
