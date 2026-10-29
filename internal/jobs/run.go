package jobs

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// Run is the handle a handler gets for the attempt it runs. The job's row is
// Run.Job as claimed; Checkpoint is the state the previous attempt persisted.
type Run struct {
	Job
	w *Worker
}

// DB is the main pool, for the handler's own statements.
func (r *Run) DB() q.DBTX { return r.w.o.Main }

// LoadCheckpoint decodes the checkpoint of an earlier attempt into v. It
// reports false if there is none. A job released at shutdown resumes from it on
// another worker with its attempt count unchanged.
func (r *Run) LoadCheckpoint(v any) (bool, error) {
	if len(r.Checkpoint) == 0 || string(r.Checkpoint) == "null" {
		return false, nil
	}
	if err := json.Unmarshal(r.Checkpoint, v); err != nil {
		return false, fmt.Errorf("jobs: checkpoint of %s: %w", r.ID, err)
	}
	return true, nil
}

// SaveCheckpoint persists the resumable state of the handler (its cursor). It
// fails with [ErrLeaseLost] if the worker no longer owns the job; the handler
// must then stop. Long handlers, such as garbage collection and data
// migrations, save after every batch.
func (r *Run) SaveCheckpoint(ctx context.Context, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("jobs: checkpoint of %s: %w", r.ID, err)
	}
	n, err := q.New(r.w.o.Main).SaveJobCheckpoint(ctx, q.SaveJobCheckpointParams{
		Checkpoint: string(b), ID: r.ID, LockedBy: r.w.o.UID,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrLeaseLost
	}
	return nil
}

// SaveProgress records progress for the job API. total 0 means unknown.
func (r *Run) SaveProgress(ctx context.Context, done, total int64, unit string) error {
	n, err := q.New(r.w.o.Main).SaveJobProgress(ctx, q.SaveJobProgressParams{
		Done: done, Total: total, Unit: unit, ID: r.ID, LockedBy: r.w.o.UID,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrLeaseLost
	}
	return nil
}

// SetResult sets the result stored when the handler returns nil.
func (r *Run) SetResult(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("jobs: result of %s: %w", r.ID, err)
	}
	r.w.setResult(r.ID, string(b))
	return nil
}
