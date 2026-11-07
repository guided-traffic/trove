// Package runstoretest is an in-memory [backup.RunStore] for the tests of the operations that write run rows: it keeps the
// rules the operations depend on (one active create or delete run, expired runs abandoned by the next Begin, fenced state
// changes, the lease margin of Fence) without a database.
package runstoretest

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/format"
)

// Mem is the store. The zero value is ready to use.
type Mem struct {
	mu   sync.Mutex
	runs []*backup.Run
	// Now returns the clock; nil is time.Now.
	Now func() time.Time
	// FenceRefused makes Fence report that the lease has no room left.
	FenceRefused bool
	// Fences counts the calls of Fence.
	Fences int
}

var _ backup.RunStore = (*Mem)(nil)

func (m *Mem) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// Runs returns copies of all rows, oldest first.
func (m *Mem) Runs() []backup.Run {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]backup.Run, len(m.runs))
	for i, r := range m.runs {
		out[i] = *r
	}
	return out
}

func (m *Mem) find(id string) *backup.Run {
	for _, r := range m.runs {
		if r.ID == id {
			return r
		}
	}
	return nil
}

func isWriting(op backup.Operation) bool { return op == backup.OpCreate || op == backup.OpDelete }

// Begin implements backup.RunStore.
func (m *Mem) Begin(_ context.Context, n backup.NewRun) (backup.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	for _, r := range m.runs {
		if r.State.Active() && r.LeaseExpiresAt.Before(now) {
			r.State, r.Hold, r.FinishedAt = backup.StateAbandoned, backup.HoldNone, &now
		}
	}
	if isWriting(n.Operation) {
		for _, r := range m.runs {
			if r.State.Active() && isWriting(r.Operation) {
				return backup.Run{}, &backup.ErrSlotTaken{Active: *r}
			}
		}
	}
	r := &backup.Run{
		ID: n.ID, Operation: n.Operation, BackupID: n.BackupID, State: backup.StateStarting, Hold: backup.HoldNone,
		Trigger: n.Trigger, Target: n.Target, Pod: n.Pod, TroveVersion: n.TroveVersion,
		StartedAt: now, HeartbeatAt: now, LeaseExpiresAt: now.Add(n.Lease),
	}
	m.runs = append(m.runs, r)
	return *r, nil
}

// Get implements backup.RunStore.
func (m *Mem) Get(_ context.Context, id string) (backup.Run, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r := m.find(id); r != nil {
		return *r, true, nil
	}
	return backup.Run{}, false, nil
}

// Active implements backup.RunStore.
func (m *Mem) Active(context.Context) (backup.Run, bool, time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.runs {
		if r.State.Active() && isWriting(r.Operation) {
			return *r, true, m.now(), nil
		}
	}
	return backup.Run{}, false, m.now(), nil
}

// Heartbeat implements backup.RunStore.
func (m *Mem) Heartbeat(_ context.Context, id string, expected backup.State, p backup.Progress, snap *time.Time, renew bool, lease time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.find(id)
	if r == nil || r.State != expected {
		return backup.ErrSuperseded
	}
	r.HeartbeatAt, r.Progress = m.now(), p
	if snap != nil {
		r.SnapshotAt = snap
	}
	if renew {
		r.LeaseExpiresAt = m.now().Add(lease)
	}
	return nil
}

// SetState implements backup.RunStore.
func (m *Mem) SetState(_ context.Context, id string, from, to backup.State, backupID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.find(id)
	if r == nil || r.State != from {
		return backup.ErrSuperseded
	}
	r.State = to
	if backupID != "" {
		r.BackupID = backupID
	}
	return nil
}

// SetHold implements backup.RunStore.
func (m *Mem) SetHold(_ context.Context, id string, from, to backup.Hold) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.find(id)
	if r == nil || r.Hold != from {
		return backup.ErrHoldLost
	}
	r.Hold = to
	return nil
}

// Fence implements backup.RunStore.
func (m *Mem) Fence(_ context.Context, id string, from, to backup.State, margin time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Fences++
	r := m.find(id)
	if r == nil || r.State != from || m.FenceRefused || !r.LeaseExpiresAt.After(m.now().Add(margin)) {
		return false, nil
	}
	r.State = to
	return true, nil
}

// AddPins implements backup.RunStore.
func (m *Mem) AddPins(context.Context, string, []format.BlobEntry) error { return nil }

// DeletePins implements backup.RunStore.
func (m *Mem) DeletePins(context.Context, string, []format.Digest) error { return nil }

// Finish implements backup.RunStore.
func (m *Mem) Finish(_ context.Context, id string, state backup.State, result, runErr json.RawMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.find(id)
	if r == nil {
		return fmt.Errorf("runstoretest: no run %s", id)
	}
	now := m.now()
	r.State, r.Hold, r.FinishedAt = state, backup.HoldNone, &now
	r.Result, r.Error = result, runErr
	return nil
}

// PruneHistory implements backup.RunStore.
func (m *Mem) PruneHistory(_ context.Context, older time.Duration) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var kept []*backup.Run
	var n int64
	for _, r := range m.runs {
		if r.FinishedAt != nil && m.now().Sub(*r.FinishedAt) > older {
			n++
			continue
		}
		kept = append(kept, r)
	}
	m.runs = kept
	return n, nil
}

// List implements the part of backup.RunReader that the sweep clock reads: runs of an operation, newest first.
func (m *Mem) List(_ context.Context, f backup.ListFilter) ([]backup.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []backup.Run
	for i := len(m.runs) - 1; i >= 0; i-- {
		r := m.runs[i]
		if f.Operation == "" || string(r.Operation) == f.Operation {
			out = append(out, *r)
		}
		if f.Limit > 0 && len(out) >= f.Limit {
			break
		}
	}
	return out, nil
}
