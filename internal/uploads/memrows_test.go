package uploads

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/guided-traffic/trove/internal/meta"
)

// memRows simulates the tables for the unit tests of the state machine: every method is one atomic step, as one transaction of
// pgRows is, and the lease and the age are read from a clock the test moves. The step of the item commit (markDone) is held back until
// the test says the commit transaction committed (commitTx) or rolled back (rollbackTx), as a transaction does. The integration tier runs
// the same service against PostgreSQL.
type memRows struct {
	mu       sync.Mutex
	now      time.Time
	sessions map[uuid.UUID]*memSession
	pending  map[uuid.UUID]int64 // markDone calls of the open commit transaction

	// afterRecordEnd, when set, makes recordEnd apply its change and then fail with the error: a COMMIT that landed while the client
	// saw an error (a switchover, a cancelled request).
	afterRecordEnd error
}

type memSession struct {
	row
	writerUntil time.Time
	created     time.Time
	activity    time.Time
	parts       map[int]part
	tails       []tailSeg
	cleaned     bool
}

func newMemRows() *memRows {
	return &memRows{now: time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC), sessions: map[uuid.UUID]*memSession{}, pending: map[uuid.UUID]int64{}}
}

func (m *memRows) advance(d time.Duration) {
	m.mu.Lock()
	m.now = m.now.Add(d)
	m.mu.Unlock()
}

func (m *memRows) view(s *memSession, maxAge time.Duration) row {
	r := s.row
	r.Claimed = r.Writer != "" && s.writerUntil.After(m.now)
	r.TooOld = m.now.Sub(s.created) > maxAge
	r.Parts = len(s.parts)
	r.HashState, r.Digest = bytes.Clone(r.HashState), bytes.Clone(r.Digest)
	r.Tails = slices.Clone(s.tails)
	return r
}

func (m *memRows) insert(_ context.Context, r row, maxOpen int) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	open := 0
	for _, o := range m.sessions {
		if o.RepoID == r.RepoID && o.Principal == r.Principal && (o.State == StateOpen || o.State == StateCompleting || o.State == StateAssembled) {
			open++
		}
	}
	if open >= maxOpen {
		return false, nil
	}
	r.State = StateOpen
	m.sessions[r.ID] = &memSession{row: r, created: m.now, activity: m.now, parts: map[int]part{}}
	return true, nil
}

func (m *memRows) get(_ context.Context, id uuid.UUID, maxAge time.Duration) (row, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return row{}, false, nil
	}
	return m.view(s, maxAge), true, nil
}

func (m *memRows) claim(_ context.Context, id uuid.UUID, writer string, offset int64, lease, maxAge time.Duration) (row, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok || s.State != StateOpen || (offset >= 0 && s.Received != offset) ||
		(s.Writer != "" && s.writerUntil.After(m.now)) || m.now.Sub(s.created) > maxAge {
		return row{}, false, nil
	}
	s.Writer, s.writerUntil, s.activity = writer, m.now.Add(lease), m.now
	return m.view(s, maxAge), true, nil
}

func (m *memRows) holder(id uuid.UUID, writer string) (*memSession, bool) {
	s, ok := m.sessions[id]
	if !ok || s.State != StateOpen || s.Writer != writer {
		return nil, false
	}
	return s, true
}

func (m *memRows) recordPart(_ context.Context, id uuid.UUID, writer string, received int64, hashState []byte, lease time.Duration, p part, uploadID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.holder(id, writer)
	if !ok {
		return errLostClaim
	}
	s.Received, s.HashState = received, bytes.Clone(hashState)
	s.TailSize, s.tails = 0, nil
	if s.UploadID == "" {
		s.UploadID = uploadID
	}
	s.writerUntil, s.activity = m.now.Add(lease), m.now
	if _, dup := s.parts[p.Number]; dup {
		return fmt.Errorf("part %d is recorded already", p.Number)
	}
	s.parts[p.Number] = p
	return nil
}

func (m *memRows) renew(_ context.Context, id uuid.UUID, writer string, lease, maxAge time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.holder(id, writer)
	if !ok || m.now.Sub(s.created) > maxAge {
		return errLostClaim
	}
	s.writerUntil, s.activity = m.now.Add(lease), m.now
	return nil
}

func (m *memRows) renewCompleting(_ context.Context, id uuid.UUID, writer string, lease time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok || s.State != StateCompleting || s.Writer != writer {
		return errLostClaim
	}
	s.writerUntil, s.activity = m.now.Add(lease), m.now
	return nil
}

func (m *memRows) claimCompleting(_ context.Context, id uuid.UUID, writer string, lease time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok || s.State != StateCompleting || (s.Writer != "" && s.writerUntil.After(m.now)) {
		return false, nil
	}
	s.Writer, s.writerUntil, s.activity = writer, m.now.Add(lease), m.now
	return true, nil
}

func (m *memRows) recordEnd(_ context.Context, id uuid.UUID, writer string, e end) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.holder(id, writer)
	if !ok {
		return errLostClaim
	}
	s.Received, s.HashState, s.TailSize = e.Received, bytes.Clone(e.HashState), e.TailSize
	if e.ReplaceTails {
		s.tails = nil
	}
	if e.NewTail != nil {
		s.tails = append(s.tails, *e.NewTail)
	}
	s.Writer, s.writerUntil, s.activity = "", time.Time{}, m.now
	if e.Completing {
		s.State, s.Digest = StateCompleting, bytes.Clone(e.Digest)
	}
	return m.afterRecordEnd
}

func (m *memRows) release(_ context.Context, id uuid.UUID, writer string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[id]; ok && s.Writer == writer {
		s.Writer, s.writerUntil = "", time.Time{}
	}
	return nil
}

func (m *memRows) parts(_ context.Context, id uuid.UUID) ([]part, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return nil, nil
	}
	out := make([]part, 0, len(s.parts))
	for _, p := range s.parts {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Number < out[j].Number })
	return out, nil
}

func (m *memRows) completeTail(_ context.Context, id uuid.UUID, writer string, p part) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok || s.State != StateCompleting || s.Writer != writer {
		return errLostClaim
	}
	s.TailSize, s.tails = 0, nil
	s.Writer, s.writerUntil, s.activity = "", time.Time{}, m.now
	if _, dup := s.parts[p.Number]; dup {
		return fmt.Errorf("part %d is recorded already", p.Number)
	}
	s.parts[p.Number] = p
	return nil
}

func (m *memRows) setAssembled(_ context.Context, id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[id]; ok && s.State == StateCompleting {
		s.State, s.activity = StateAssembled, m.now
	}
	return nil
}

// markDone holds the end of the session back until the commit transaction commits.
func (m *memRows) markDone(_ context.Context, _ meta.Tx, id uuid.UUID, itemID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok || (s.State != StateAssembled && s.State != StateDone) {
		return errAborted
	}
	m.pending[id] = itemID
	return nil
}

// commitTx applies the markDone calls of the commit transaction that committed.
func (m *memRows) commitTx() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, item := range m.pending {
		s := m.sessions[id]
		s.State, s.ItemID, s.Writer, s.writerUntil, s.activity = StateDone, item, "", time.Time{}, m.now
	}
	clear(m.pending)
}

// rollbackTx drops the markDone calls of a commit transaction that rolled back.
func (m *memRows) rollbackTx() {
	m.mu.Lock()
	defer m.mu.Unlock()
	clear(m.pending)
}

func (m *memRows) markCleaned(_ context.Context, id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[id]; ok {
		s.cleaned = true
	}
	return nil
}

func (m *memRows) deleteCleaned(_ context.Context, id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[id]; ok && (s.State == StateAborted || s.State == StateDone) {
		delete(m.sessions, id)
	}
	return nil
}

func (m *memRows) abort(_ context.Context, id uuid.UUID) (bool, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok || (s.State != StateOpen && s.State != StateCompleting && s.State != StateAssembled) {
		return false, "", nil
	}
	s.State, s.Writer, s.writerUntil, s.activity = StateAborted, "", time.Time{}, m.now
	return true, s.UploadID, nil
}

// state is the recorded state of a session, for assertions.
// stateGone is the state of a session whose row was deleted.
const stateGone State = ""

func (m *memRows) state(id uuid.UUID) State {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[id]; ok {
		return s.State
	}
	return stateGone
}

// tails are the recorded tail segments of a session, for assertions.
func (m *memRows) tailsOf(id uuid.UUID) []tailSeg {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.sessions[id].tails)
}
