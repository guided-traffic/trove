package backup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Timing is the lease and heartbeat configuration of a run (backup.lease_duration, heartbeat_interval, stall_timeout,
// finalize_timeout; R-BAK-011).
type Timing struct {
	Lease     time.Duration
	Heartbeat time.Duration
	Stall     time.Duration
	Finalize  time.Duration
}

// Handle is a running operation's side of its backup_run row: it renews the lease while the run makes progress, cancels the
// run's context when the run was superseded or stalled, and fences the transitions that must not happen after the lease
// ran out (R-BAK-010, R-BAK-013, Q-BAK-11). Every operation that writes a run row (create, delete, verify) holds one, so a
// dead run of any operation ends abandoned.
//
// The context that [StartHandle] returns is cancelled with the cause [ErrSuperseded] or [ErrStalled]; code that waits on
// the target or the database passes it down and reports progress with [Handle.Tick] or [Handle.Update].
type Handle struct {
	store RunStore
	id    string
	t     Timing
	log   *slog.Logger
	now   func() time.Time

	cancel context.CancelCauseFunc
	stop   chan struct{}
	done   chan struct{}

	// move serialises a state change and a heartbeat: the heartbeat is fenced on the state, so one that read the old state
	// while Transition or Fence already moved the row would match no row and falsely read as a superseded run.
	move sync.Mutex

	mu         sync.Mutex
	state      State
	progress   Progress
	snapshotAt *time.Time
	ticks      uint64
	seen       uint64
	lastTick   time.Time
}

// StartHandle starts the heartbeat of a run that Begin returned. Stop it with [Handle.Stop] before Finish.
func StartHandle(ctx context.Context, store RunStore, run Run, t Timing, log *slog.Logger) (*Handle, context.Context) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	ctx, cancel := context.WithCancelCause(ctx)
	h := &Handle{
		store: store, id: run.ID, t: t, log: log, now: time.Now, cancel: cancel,
		stop: make(chan struct{}), done: make(chan struct{}), state: run.State, progress: run.Progress, lastTick: time.Now(),
	}
	go h.loop(ctx)
	return h, ctx
}

// ID is the run's ID.
func (h *Handle) ID() string { return h.id }

// State is the state the handle last moved the run to.
func (h *Handle) State() State {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.state
}

// Tick records that the run made progress: an exported row batch, an inserted pin batch, a confirmed object or part, a
// listing page or deletion batch, or a wait for a lock within its configured bound (R-BAK-013). The next heartbeat renews
// the lease only if there was a tick since the last one.
func (h *Handle) Tick() {
	h.mu.Lock()
	h.ticks++
	h.lastTick = h.now()
	h.mu.Unlock()
}

// Update changes the progress object and counts as a tick.
func (h *Handle) Update(fn func(*Progress)) {
	h.mu.Lock()
	fn(&h.progress)
	h.ticks++
	h.lastTick = h.now()
	h.mu.Unlock()
}

// SetSnapshotAt records the snapshot time; the next heartbeat writes it.
func (h *Handle) SetSnapshotAt(t time.Time) {
	h.mu.Lock()
	h.snapshotAt = &t
	h.mu.Unlock()
}

// Progress returns a copy of the progress object.
func (h *Handle) Progress() Progress {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.progress
}

// Transition moves the run to the next active state.
func (h *Handle) Transition(ctx context.Context, to State, backupID string) error {
	h.move.Lock()
	defer h.move.Unlock()
	h.mu.Lock()
	from := h.state
	h.mu.Unlock()
	if err := h.store.SetState(ctx, h.id, from, to, backupID); err != nil {
		return err
	}
	h.mu.Lock()
	h.state = to
	h.progress.Phase = string(to)
	h.mu.Unlock()
	h.Tick()
	return nil
}

// SetHold changes the run's hold (R-BAK-007).
func (h *Handle) SetHold(ctx context.Context, from, to Hold) error {
	if err := h.store.SetHold(ctx, h.id, from, to); err != nil {
		return err
	}
	h.Tick()
	return nil
}

// Fence moves the run to the given state only while its lease has room for two finalize timeouts, the check before the
// manifest and before every deletion of the pruning phase (R-BAK-010). A run that fails the fence has lost its slot or is
// about to; it returns [ErrSuperseded] and writes nothing.
func (h *Handle) Fence(ctx context.Context, to State) error {
	h.move.Lock()
	defer h.move.Unlock()
	h.mu.Lock()
	from := h.state
	h.mu.Unlock()
	ok, err := h.store.Fence(ctx, h.id, from, to, 2*h.t.Finalize)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: the lease has less than %s left", ErrSuperseded, 2*h.t.Finalize)
	}
	h.mu.Lock()
	h.state = to
	h.mu.Unlock()
	h.Tick()
	return nil
}

// Stop ends the heartbeat and waits for it. Calling it twice is harmless.
func (h *Handle) Stop() {
	select {
	case <-h.stop:
	default:
		close(h.stop)
	}
	<-h.done
}

func (h *Handle) loop(ctx context.Context) {
	defer close(h.done)
	defer h.cancel(nil)
	tk := time.NewTicker(h.t.Heartbeat)
	defer tk.Stop()
	var failingSince time.Time
	for {
		select {
		case <-h.stop:
			return
		case <-ctx.Done():
			return
		case <-tk.C:
		}
		h.move.Lock()
		h.mu.Lock()
		renew := h.ticks != h.seen
		h.seen = h.ticks
		idle := h.now().Sub(h.lastTick)
		state, p, snap := h.state, h.progress, h.snapshotAt
		h.mu.Unlock()
		if !renew && idle >= h.t.Stall {
			h.move.Unlock()
			h.log.Error("backup run made no progress", "stall_timeout", h.t.Stall, "run", h.id)
			h.cancel(ErrStalled)
			return
		}
		err := h.store.Heartbeat(ctx, h.id, state, p, snap, renew, h.t.Lease)
		h.move.Unlock()
		switch {
		case err == nil:
			failingSince = time.Time{}
		case errors.Is(err, ErrSuperseded):
			h.log.Error("backup run was superseded", "run", h.id)
			h.cancel(ErrSuperseded)
			return
		case ctx.Err() != nil:
			return
		default:
			now := h.now()
			if failingSince.IsZero() {
				failingSince = now
			}
			h.log.Warn("backup heartbeat failed", "run", h.id, "err", err)
			if now.Sub(failingSince) > h.t.Lease {
				h.cancel(fmt.Errorf("backup: the heartbeat failed for longer than the lease: %w", err))
				return
			}
		}
	}
}
