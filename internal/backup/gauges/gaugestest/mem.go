// Package gaugestest is an in-memory backup.RunReader for the tests of the API and the gauges. It orders and filters like the
// statements of queries/backup.sql, counts its calls, and takes the database's clock from its Now field.
package gaugestest

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/backup"
)

// Mem holds runs and answers the reads of backup.RunReader.
type Mem struct {
	mu sync.Mutex
	// Now is the database's clock.
	Now  time.Time
	Runs []backup.Run
	// Hold says which holds exist, regardless of the runs.
	HoldAll, HoldPinned bool
	// JournalRows and JournalOldest answer JournalPending.
	JournalRows   int64
	JournalOldest time.Duration
	// Err is returned by every read when set.
	Err error
	// Calls counts the reads by method name.
	Calls map[string]int
}

var _ backup.RunReader = (*Mem)(nil)

func (m *Mem) call(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Calls == nil {
		m.Calls = map[string]int{}
	}
	m.Calls[name]++
	return m.Err
}

// Total is the number of reads so far.
func (m *Mem) Total() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, c := range m.Calls {
		n += c
	}
	return n
}

// Get implements backup.RunReader.
func (m *Mem) Get(_ context.Context, id string) (backup.Run, bool, error) {
	if err := m.call("Get"); err != nil {
		return backup.Run{}, false, err
	}
	for _, r := range m.Runs {
		if r.ID == id {
			return r, true, nil
		}
	}
	return backup.Run{}, false, nil
}

func (m *Mem) matching(f backup.ListFilter) []backup.Run {
	var out []backup.Run
	for _, r := range m.Runs {
		switch {
		case f.Operation != "" && string(r.Operation) != f.Operation,
			f.State != "" && string(r.State) != f.State,
			f.Trigger != "" && string(r.Trigger) != f.Trigger,
			!f.StartedFrom.IsZero() && r.StartedAt.Before(f.StartedFrom),
			!f.StartedTo.IsZero() && !r.StartedAt.Before(f.StartedTo):
			continue
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].StartedAt.After(out[j].StartedAt)
		}
		return out[i].ID > out[j].ID
	})
	return out
}

// List implements backup.RunReader: newest first, keyset after the cursor.
func (m *Mem) List(_ context.Context, f backup.ListFilter) ([]backup.Run, error) {
	if err := m.call("List"); err != nil {
		return nil, err
	}
	var out []backup.Run
	for _, r := range m.matching(f) {
		if f.After != nil && !r.StartedAt.Before(f.After.StartedAt) && (!r.StartedAt.Equal(f.After.StartedAt) || r.ID >= f.After.ID) {
			continue
		}
		out = append(out, r)
		if f.Limit > 0 && len(out) == f.Limit {
			break
		}
	}
	return out, nil
}

// Count implements backup.RunReader.
func (m *Mem) Count(_ context.Context, f backup.ListFilter) (int64, error) {
	if err := m.call("Count"); err != nil {
		return 0, err
	}
	return int64(len(m.matching(f))), nil
}

// Last implements backup.RunReader: the newest finished run of an operation among the states.
func (m *Mem) Last(_ context.Context, op backup.Operation, states ...backup.State) (backup.Run, bool, error) {
	if err := m.call("Last"); err != nil {
		return backup.Run{}, false, err
	}
	var best *backup.Run
	for i := range m.Runs {
		r := &m.Runs[i]
		if r.Operation != op || r.FinishedAt == nil {
			continue
		}
		in := false
		for _, s := range states {
			in = in || s == r.State
		}
		if in && (best == nil || r.FinishedAt.After(*best.FinishedAt)) {
			best = r
		}
	}
	if best == nil {
		return backup.Run{}, false, nil
	}
	return *best, true, nil
}

// ActiveRuns implements backup.RunReader.
func (m *Mem) ActiveRuns(context.Context) ([]backup.Run, time.Time, error) {
	if err := m.call("ActiveRuns"); err != nil {
		return nil, time.Time{}, err
	}
	var out []backup.Run
	for _, r := range m.matching(backup.ListFilter{}) {
		if r.State.Active() {
			out = append(out, r)
		}
	}
	return out, m.Now, nil
}

// HoldValid implements backup.RunReader.
func (m *Mem) HoldValid(_ context.Context, h backup.Hold) (bool, error) {
	if err := m.call("HoldValid"); err != nil {
		return false, err
	}
	if h == backup.HoldAll {
		return m.HoldAll, nil
	}
	return m.HoldPinned, nil
}

// JournalPending implements backup.RunReader.
func (m *Mem) JournalPending(context.Context) (int64, time.Duration, error) {
	if err := m.call("JournalPending"); err != nil {
		return 0, 0, err
	}
	return m.JournalRows, m.JournalOldest, nil
}
