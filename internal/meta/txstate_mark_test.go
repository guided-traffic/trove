package meta

import (
	"context"
	"testing"
)

// TestR_API_014_DiscardForgetsTheHooksOfUndoneWork: the hooks of work that was
// rolled back to a savepoint neither write at commit nor keep what they hold.
func TestR_API_014_DiscardForgetsTheHooksOfUndoneWork(t *testing.T) {
	var s TxState
	var ran []string
	s.Before(func(context.Context, Tx) error { ran = append(ran, "before-kept"); return nil })
	mark := s.Mark()
	s.Before(func(context.Context, Tx) error { ran = append(ran, "before-dropped"); return nil })
	s.After(func() { ran = append(ran, "after-dropped") })
	s.OnRollback(func() { ran = append(ran, "rollback-ran") })
	s.Discard(mark)
	if len(ran) != 1 || ran[0] != "rollback-ran" {
		t.Fatalf("after Discard: %v", ran)
	}
	if err := s.RunBefore(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	s.RunAfter()
	if len(ran) != 2 || ran[1] != "before-kept" {
		t.Fatalf("at commit: %v", ran)
	}
}
