package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// A PostgreSQL switchover ends every connection for some seconds. The two writes a handler makes while it runs, its checkpoint and its
// progress, ride it out: they repeat on a failover error with a growing pause for up to failoverWindow, so the attempt is not lost
// (R-HA-059 variant C: every job kept its attempt; the first run of E5.9 showed attempt 2 on all six probe jobs).
const (
	failoverWindow = 30 * time.Second
	failoverPause  = 200 * time.Millisecond
	failoverMax    = 2 * time.Second
)

// retryFailover runs write until it does not fail with a failover error, the window is used up or ctx ends.
func retryFailover(ctx context.Context, write func() (int64, error)) (int64, error) {
	deadline := time.Now().Add(failoverWindow)
	pause := failoverPause
	for {
		n, err := write()
		if err == nil || !postgres.IsFailoverError(err) || time.Now().Add(pause).After(deadline) {
			return n, err
		}
		select {
		case <-ctx.Done():
			return n, err
		case <-time.After(pause):
		}
		pause = min(pause*2, failoverMax)
	}
}

// Run is the handle a handler gets for the attempt it runs. The job's row is
// Run.Job as claimed; Checkpoint is the state the previous attempt persisted.
type Run struct {
	Job
	w  *Worker
	rj *runningJob
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
	n, err := retryFailover(ctx, func() (int64, error) {
		return q.New(r.w.o.Main).SaveJobCheckpoint(ctx, q.SaveJobCheckpointParams{
			Checkpoint: string(b), ID: r.ID, LockedBy: r.w.o.UID, Attempt: int32(r.Attempt), //nolint:gosec // attempts are small
		})
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
	n, err := retryFailover(ctx, func() (int64, error) {
		return q.New(r.w.o.Main).SaveJobProgress(ctx, q.SaveJobProgressParams{
			Done: done, Total: total, Unit: unit, ID: r.ID, LockedBy: r.w.o.UID, Attempt: int32(r.Attempt), //nolint:gosec // attempts are small
		})
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
	r.w.setResult(r.rj, string(b))
	return nil
}
