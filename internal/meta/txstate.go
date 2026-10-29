package meta

import (
	"context"
	"sync"
)

// TxState implements the hook and slot half of Tx. The PostgreSQL transaction
// of internal/meta/postgres embeds it, and so do the fake transactions of unit
// tests, which then run the hooks the way a runner does with RunBefore,
// RunAfter and RunRollback.
//
// The zero value is ready to use. A TxState belongs to one attempt.
type TxState struct {
	mu       sync.Mutex
	before   []func(context.Context, Tx) error
	after    []func()
	rollback []func()
	values   map[any]any
}

// Before implements Tx.
func (s *TxState) Before(fn func(context.Context, Tx) error) {
	s.mu.Lock()
	s.before = append(s.before, fn)
	s.mu.Unlock()
}

// After implements Tx.
func (s *TxState) After(fn func()) {
	s.mu.Lock()
	s.after = append(s.after, fn)
	s.mu.Unlock()
}

// OnRollback implements Tx.
func (s *TxState) OnRollback(fn func()) {
	s.mu.Lock()
	s.rollback = append(s.rollback, fn)
	s.mu.Unlock()
}

// HookMark is a position in the hooks of a transaction attempt.
type HookMark struct{ before, after, rollback int }

// Mark returns the current position of the hooks, for [TxState.Discard].
func (s *TxState) Mark() HookMark {
	s.mu.Lock()
	defer s.mu.Unlock()
	return HookMark{len(s.before), len(s.after), len(s.rollback)}
}

// Discard forgets the Before and After hooks registered since the mark and runs
// the OnRollback hooks registered since it: the work that registered them was
// undone inside the transaction (a rollback to a savepoint), so it must neither
// write at commit nor keep what it holds. The transaction goes on.
func (s *TxState) Discard(m HookMark) {
	s.mu.Lock()
	var undo []func()
	if len(s.before) > m.before {
		s.before = s.before[:m.before]
	}
	if len(s.after) > m.after {
		s.after = s.after[:m.after]
	}
	if len(s.rollback) > m.rollback {
		undo = append(undo, s.rollback[m.rollback:]...)
		s.rollback = s.rollback[:m.rollback]
	}
	s.mu.Unlock()
	for _, fn := range undo {
		fn()
	}
}

// Value implements Tx.
func (s *TxState) Value(key any) any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.values[key]
}

// SetValue implements Tx.
func (s *TxState) SetValue(key, v any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.values == nil {
		s.values = map[any]any{}
	}
	s.values[key] = v
}

// RunBefore runs the Before hooks in registration order on tx, the transaction
// that embeds s; a hook may register another, which runs after it. The first
// error stops the run and is returned.
func (s *TxState) RunBefore(ctx context.Context, tx Tx) error {
	for i := 0; ; i++ {
		s.mu.Lock()
		if i >= len(s.before) {
			s.mu.Unlock()
			return nil
		}
		fn := s.before[i]
		s.mu.Unlock()
		if err := fn(ctx, tx); err != nil {
			return err
		}
	}
}

// RunAfter runs the After hooks and drops every hook and value: the attempt is
// over. Call it once COMMIT succeeded.
func (s *TxState) RunAfter() {
	s.mu.Lock()
	after := s.after
	s.before, s.after, s.rollback, s.values = nil, nil, nil, nil
	s.mu.Unlock()
	for _, f := range after {
		f()
	}
}

// RunRollback runs the OnRollback hooks and drops every hook and value. Call it
// when the attempt ended without a commit.
func (s *TxState) RunRollback() {
	s.mu.Lock()
	rb := s.rollback
	s.before, s.after, s.rollback, s.values = nil, nil, nil, nil
	s.mu.Unlock()
	for _, f := range rb {
		f()
	}
}
