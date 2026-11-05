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
