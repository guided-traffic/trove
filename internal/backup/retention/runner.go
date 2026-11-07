package retention

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/format"
	"github.com/guided-traffic/trove/internal/backup/listing"
)

// Runner runs `trove backup delete` and `trove backup prune` as target-writing runs of their own (R-BAK-010, R-BAK-016): it
// takes the slot, heartbeats, moves the run to state pruning, calls the stage code and writes the result. Both are
// operation delete in backup_run; a delete of one backup records its ID in backup_id.
type Runner struct {
	Stage *Stage
	Runs  backup.RunStore
	// Timing is the lease and heartbeat configuration.
	Timing backup.Timing
	// RunHistory is backup.run_history: rows older than this are deleted by the next run.
	RunHistory time.Duration
	// Target is the "s3://<bucket>/<prefix>" text of the run row; Pod and TroveVersion complete it.
	Target, Pod, TroveVersion string
	// NewRunID returns a UUIDv7.
	NewRunID func() string
	// Sleep waits for a stale run's lease; nil is a real sleep.
	Sleep func(context.Context, time.Duration) error
}

var (
	_ backup.Pruner  = (*prunerRun)(nil)
	_ backup.Deleter = (*Runner)(nil)
)

// Pruner returns the [backup.Pruner] of the runner. Prune on the runner itself would clash with the stage's name.
func (r *Runner) Pruner() backup.Pruner { return &prunerRun{r} }

type prunerRun struct{ r *Runner }

// Prune implements [backup.Pruner]: the retention policy and the pool sweep, in a run of their own.
func (p *prunerRun) Prune(ctx context.Context) (*backup.Result, error) {
	return p.r.execute(ctx, "", func(ctx context.Context, h *backup.Handle, res *backup.Result) error {
		return p.r.Stage.prune(ctx, h, res, false)
	})
}

// Delete implements [backup.Deleter]: one backup regardless of the policy, then the sweep. Deleting the only complete backup
// needs AllowLast and is otherwise refused with exit 2. The flag Yes is the command's concern: a prompt is not a thing a
// Job can answer.
func (r *Runner) Delete(ctx context.Context, opts backup.DeleteOptions) (*backup.Result, error) {
	return r.execute(ctx, opts.ID, func(ctx context.Context, h *backup.Handle, res *backup.Result) error {
		s := r.Stage
		if err := CheckMarker(ctx, s.Store, s.Layout, s.Facts); err != nil {
			return err
		}
		scan, err := listing.ScanTicking(ctx, s.Store, s.Layout, h.Tick)
		if err != nil {
			return err
		}
		var found, complete bool
		completeCount := 0
		for _, b := range scan {
			if b.Complete() {
				completeCount++
			}
			if b.ID == opts.ID {
				found, complete = true, b.Complete()
			}
		}
		switch {
		case !found:
			return backup.Failf(backup.ExitUsage, backup.CodeDeletionNotAllowed, "backup %s does not exist in the target", opts.ID)
		case complete && completeCount == 1 && !opts.AllowLast:
			return backup.Failf(backup.ExitUsage, backup.CodeDeletionNotAllowed,
				"backup %s is the only complete backup of the target; deleting it needs --allow-last", opts.ID)
		}
		res.Retention = &backup.Retention{KeepLast: s.Policy.KeepLast, KeepWithinSeconds: int64(s.Policy.KeepWithin / time.Second)}
		reason := ReasonManual
		if err := s.deleteBackup(ctx, h, opts.ID, complete, reason); err != nil {
			return err
		}
		if complete {
			res.BackupsDeleted = append(res.BackupsDeleted, string(opts.ID))
		} else {
			res.IncompleteDeleted = append(res.IncompleteDeleted, string(opts.ID))
		}
		return s.finish(ctx, h, res, scan, true, false)
	})
}

// execute is the run around a body: the checks that write nothing, the slot, the heartbeat, the move to pruning and the
// end state with its result (R-BAK-009, R-BAK-013).
func (r *Runner) execute(ctx context.Context, id format.ID, body func(context.Context, *backup.Handle, *backup.Result) error) (*backup.Result, error) {
	s := r.Stage
	facts, err := s.Facts.SystemFacts(ctx)
	if err != nil {
		return nil, backup.Failf(backup.ExitTemporary, backup.CodeTemporary, "reading the installation facts: %w", err)
	}
	if facts.RestoreInProgress || facts.SchemaBehind {
		return nil, backup.ErrRestoreInProgress
	}
	run, err := backup.AcquireSlot(ctx, r.Runs, backup.NewRun{
		ID: r.NewRunID(), Operation: backup.OpDelete, BackupID: string(id), Trigger: backup.TriggerManual,
		Target: r.Target, Pod: r.Pod, TroveVersion: r.TroveVersion, Lease: r.Timing.Lease,
	}, r.Timing, r.Sleep)
	if err != nil {
		return nil, backup.Classify(err)
	}
	started := time.Now()
	h, hctx := backup.StartHandle(ctx, r.Runs, run, r.Timing, s.log())
	defer h.Stop()

	if r.RunHistory > 0 {
		if n, herr := r.Runs.PruneHistory(hctx, r.RunHistory); herr != nil {
			s.log().Warn("deleting old backup runs failed", "err", herr)
		} else if n > 0 {
			s.log().Info("old backup runs deleted", "count", n)
		}
	}
	res := &backup.Result{}
	err = h.Transition(hctx, backup.StatePruning, string(id))
	if err == nil {
		err = body(hctx, h, res)
	}
	h.Stop()
	res.DurationSeconds = int64(time.Since(started) / time.Second)
	return r.finish(ctx, hctx, run.ID, res, err)
}

// finish writes the end state of the run and maps the error to its exit.
func (r *Runner) finish(ctx, hctx context.Context, runID string, res *backup.Result, runErr error) (*backup.Result, error) {
	runErr = backup.Classify(runErr)
	cause := context.Cause(hctx)
	if runErr != nil && cause != nil && (errors.Is(cause, backup.ErrSuperseded) || errors.Is(cause, backup.ErrStalled)) {
		runErr = fmt.Errorf("%w: %w", cause, runErr)
	}
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()

	if runErr == nil {
		state := backup.StateCompleted
		if len(res.Warnings) > 0 {
			state = backup.StateCompletedWithWarning
		}
		res.Outcome = string(state)
		raw, err := json.Marshal(res)
		if err == nil {
			err = r.Runs.Finish(fctx, runID, state, raw, nil)
		}
		if err != nil {
			return nil, fmt.Errorf("recording the end of the run: %w", err)
		}
		return res, nil
	}
	if errors.Is(runErr, backup.ErrSuperseded) {
		// Another run took the slot: the row is no longer ours to write.
		return nil, runErr
	}
	state, failure := backup.StateFailed, backup.ErrorCode(runErr)
	switch {
	case errors.Is(runErr, backup.ErrStalled):
		runErr = backup.Fail(backup.ExitTemporary, backup.CodeStalled, runErr)
	case ctx.Err() != nil:
		state, failure = backup.StateInterrupted, backup.CodeInterrupted
		runErr = backup.Fail(backup.ExitTemporary, backup.CodeInterrupted, runErr)
	}
	body, _ := json.Marshal(map[string]string{"code": failure, "message": runErr.Error()})
	raw, _ := json.Marshal(res)
	if err := r.Runs.Finish(fctx, runID, state, raw, body); err != nil {
		r.Stage.log().Error("recording the failure of the run failed", "err", err, "run_error", runErr)
	}
	return nil, runErr
}
