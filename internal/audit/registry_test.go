package audit

import "testing"

func TestR_OPS_014_RegisterRules(t *testing.T) {
	good := EventType{Name: "probe.thing.created", Category: CategorySystem, Mode: Transactional, Schema: `{"type":"object"}`}
	cases := []struct {
		name string
		mut  func(*EventType)
	}{
		{"bad name", func(e *EventType) { e.Name = "Probe" }},
		{"one segment", func(e *EventType) { e.Name = "probe" }},
		{"bad category", func(e *EventType) { e.Category = "x" }},
		{"bad mode", func(e *EventType) { e.Mode = "x" }},
		{"journal on async", func(e *EventType) { e.Mode = Async; e.Journal = JournalRevocation }},
		{"no schema", func(e *EventType) { e.Schema = "" }},
	}
	for _, c := range cases {
		t.Run("R-OPS-014 "+c.name, func(t *testing.T) {
			e := good
			c.mut(&e)
			defer func() {
				if recover() == nil {
					t.Fatal("Register did not panic")
				}
			}()
			Register(e)
		})
	}
}
