// Package create is `trove backup create` (R-BAK-009): the pipeline that takes the consistent snapshot of the database,
// exports its tables, copies the blobs the snapshot references into the target's pool incrementally, keeps garbage collection
// away from them while it does, and ends with the manifest, the commit point of a backup.
//
// The pipeline is written against the ports of internal/backup (run store, snapshotter, facts, blob source) and
// internal/backup/target, so every phase is tested with fakes and the integration tests run the same code against PostgreSQL
// and an S3 store. The phases follow R-BAK-009:
//
//	starting    facts, grants, run row, target preflight (marker, adoption, server-side encryption), listing
//	snapshot    shared migration lock, hold 'all', table locks, snapshot, referenced set, diff, blob list, pins, hold 'pinned'
//	copying     table export (snapshot open) and blob copy in parallel; the last pin gone, hold 'none'
//	finalizing  fenced lease check, spot check of the pool, marker check, manifest
//	pruning     retention and pool sweep through backup.PruneStage
//
// Whatever happens after the run row exists ends in Finish: completed, completed_with_warnings, failed or interrupted, with the
// hold released and the pins deleted in the same transaction.
package create

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/format"
	"github.com/guided-traffic/trove/internal/backup/tables"
	"github.com/guided-traffic/trove/internal/backup/target"
	"github.com/guided-traffic/trove/internal/config"
)

// PinReader pages through the to-copy set of a run in digest order. backup.PGFacts implements it.
type PinReader interface {
	Pins(ctx context.Context, runID string, after format.Digest, limit int) ([]format.BlobEntry, error)
}

// GrantChecker is the check of R-BAK-015 at the start of a run. backup.PGFacts implements it.
type GrantChecker interface {
	CheckGrants(ctx context.Context) error
}

// Events writes the audit events of the run into the source database (R-BAK-014). A failure is logged, never fatal: the
// backup itself is the record.
type Events interface {
	Record(ctx context.Context, ev audit.Event) error
}

// Deps are what the pipeline works with.
type Deps struct {
	Cfg config.Backup
	// SourceRef names where the blob store is (no credentials): recorded in the manifest.
	SourceRef format.StorageRef
	// Instance is the pod name, Version and Revision the build.
	Instance, Version, Revision string
	// JobName is the Kubernetes Job of the pod (TROVE_JOB_NAME); empty outside a Job (R-BAK-010).
	JobName string
	// BackupRole is database.backup_role; empty means the run uses the runtime credential and warns.
	BackupRole string

	Store     backup.RunStore
	Facts     backup.Facts
	Grants    GrantChecker
	Pins      PinReader
	Snapshot  backup.Snapshotter
	Source    backup.BlobSource
	BlobState backup.BlobStateReader
	Target    target.Store
	// Prune is the pruning phase; nil skips it.
	Prune   backup.PruneStage
	Events  Events
	Metrics *backup.Metrics
	// Registry classifies the tables; nil means tables.Default().
	Registry *tables.Registry
	Log      *slog.Logger

	// The fields below default for production and are set by tests.
	Now   func() time.Time
	Rand  io.Reader
	Sleep func(context.Context, time.Duration) error
	// Fault is called at the named points of a run (see the point constants); a non-nil error ends the run there as a failure.
	// An integration test pauses a run between two steps of the hold protocol with it.
	Fault func(ctx context.Context, point string) error
	// AbortTimeout bounds each abort of an open upload when a run ends, so that a target that does not answer cannot keep the run
	// past the grace period of its pod; 0 means 8 s.
	AbortTimeout time.Duration
	// PinBatch is the number of pins inserted per transaction (Q-BAK-17); 0 means 2000.
	PinBatch int
}

// The named points of Deps.Fault, in the order a run passes them.
const (
	PointHoldAll       = "hold-all"       // the 'all' hold is committed, the snapshot is not taken
	PointSnapshot      = "snapshot"       // the snapshot is open
	PointPinsCommitted = "pins-committed" // every pin is committed, the hold is still 'all'
	PointHoldPinned    = "hold-pinned"    // the hold is 'pinned'
	PointCopied        = "copied"         // every pin is gone, the hold is 'none'
	PointFinalizing    = "finalizing"     // before the spot check and the manifest
)

// Pipeline implements backup.Creator.
type Pipeline struct{ d Deps }

var _ backup.Creator = (*Pipeline)(nil)

// New validates the dependencies and builds the pipeline.
func New(d Deps) (*Pipeline, error) {
	for name, nilDep := range map[string]bool{
		"Store": d.Store == nil, "Facts": d.Facts == nil, "Grants": d.Grants == nil, "Pins": d.Pins == nil, "Snapshot": d.Snapshot == nil,
		"Source": d.Source == nil, "BlobState": d.BlobState == nil, "Target": d.Target == nil,
	} {
		if nilDep {
			return nil, fmt.Errorf("create: dependency %s is missing", name)
		}
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Registry == nil {
		d.Registry = tables.Default()
	}
	if d.AbortTimeout <= 0 {
		d.AbortTimeout = 8 * time.Second
	}
	if d.PinBatch <= 0 {
		d.PinBatch = 2000
	}
	if d.Instance == "" {
		d.Instance, _ = os.Hostname()
	}
	return &Pipeline{d: d}, nil
}

func (p *Pipeline) timing() backup.Timing {
	c := p.d.Cfg
	return backup.Timing{Lease: c.LeaseDuration, Heartbeat: c.HeartbeatInterval, Stall: c.StallTimeout, Finalize: c.FinalizeTimeout}
}

// Create implements backup.Creator: one backup, from the checks to the result.
func (p *Pipeline) Create(ctx context.Context, opts backup.CreateOptions) (*backup.Result, error) {
	d := p.d
	if opts.Trigger == "" {
		opts.Trigger = backup.TriggerManual
	}
	// Nothing is written before these checks pass (R-BAK-009, R-BAK-015).
	facts, err := d.Facts.SystemFacts(ctx)
	if err != nil {
		return nil, classify(fmt.Errorf("read the system facts: %w", err))
	}
	if facts.RestoreInProgress || facts.SchemaBehind {
		return nil, backup.Fail(backup.ExitTemporary, backup.CodeRestoreInProgress, backup.ErrRestoreInProgress)
	}
	if err := d.Grants.CheckGrants(ctx); err != nil {
		return nil, classify(err)
	}
	if d.BackupRole == "" {
		d.Log.Warn("database.backup_role is not set: the run uses the runtime database credential (R-BAK-015)")
	}
	started := d.Now()
	id, err := format.NewID(started, d.Rand)
	if err != nil {
		return nil, err
	}
	layout := format.Layout{Prefix: d.Cfg.Target.Prefix}
	runID, err := newRunID(started)
	if err != nil {
		return nil, err
	}
	run, err := backup.AcquireSlot(ctx, d.Store, backup.NewRun{
		ID: runID, Operation: backup.OpCreate, BackupID: string(id), Trigger: opts.Trigger, Target: targetName(d.Cfg.Target),
		Pod: d.Instance, TroveVersion: d.Version, JobName: d.JobName, Lease: d.Cfg.LeaseDuration,
	}, p.timing(), d.Sleep)
	if err != nil {
		return nil, classify(err)
	}
	if n, err := d.Store.PruneHistory(ctx, d.Cfg.RunHistory); err != nil {
		d.Log.Warn("could not delete old backup runs", "err", err)
	} else if n > 0 {
		d.Log.Info("deleted old backup runs", "count", n)
	}

	h, hctx := backup.StartHandle(ctx, d.Store, run, p.timing(), d.Log)
	r := &runState{p: p, d: d, h: h, opts: opts, id: id, runID: runID, layout: layout, facts: facts, started: started, res: &backup.Result{BackupID: string(id)}}
	d.Log.Info("backup run started", "run", runID, "backup", id, "trigger", opts.Trigger)

	err = r.execute(hctx)
	h.Stop()
	return r.finish(ctx, hctx, err)
}

func targetName(t config.BackupTarget) string {
	host := t.Endpoint
	if host == "" {
		host = "aws"
	}
	return fmt.Sprintf("s3://%s/%s (%s)", t.Bucket, t.Prefix, host)
}

// finish ends the run row and the audit trail, and maps the outcome to the result and the error of the command.
func (r *runState) finish(ctx, hctx context.Context, err error) (*backup.Result, error) {
	// The cleanup uses a context of its own: the run's may be the thing that ended.
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	r.cleanup(cctx)

	if err == nil {
		state := backup.StateCompleted
		if r.res.DamagedBlobs > 0 || r.pruneWarned {
			state = backup.StateCompletedWithWarning
		}
		r.res.Outcome = string(state)
		r.res.DurationSeconds = int64(r.d.Now().Sub(r.started).Seconds())
		raw, _ := marshal(r.res)
		var ferr error
		for attempt := 0; attempt < 3; attempt++ {
			if ferr = r.d.Store.Finish(cctx, r.runID, state, raw, nil); ferr == nil || errors.Is(ferr, backup.ErrSuperseded) {
				break
			}
			time.Sleep(time.Duration(attempt+1) * 500 * time.Millisecond)
		}
		if ferr != nil {
			// The manifest exists: the backup is complete. The run row is what is wrong.
			r.d.Log.Error("the backup is complete but its run record could not be finished", "backup", r.id, "err", ferr)
			return nil, classify(fmt.Errorf("finish the run record: %w", ferr))
		}
		r.event(cctx, audit.Event{
			Type: audit.EventBackupCompleted, Actor: actor(), Outcome: audit.Success,
			Details: mustJSON(map[string]any{"backupId": string(r.id), "outcome": string(state), "databaseBytes": r.res.DatabaseBytes, "blobsCopied": r.res.BlobsCopied, "damaged": r.res.DamagedBlobs}),
		})
		r.d.Log.Info("backup run finished", "backup", r.id, "outcome", state, "copied", r.res.BlobsCopied, "damaged", r.res.DamagedBlobs)
		return r.res, nil
	}

	cause := err
	if c := context.Cause(hctx); c != nil && (errors.Is(c, backup.ErrStalled) || errors.Is(c, backup.ErrSuperseded)) && !errors.Is(err, c) {
		cause = fmt.Errorf("%w: %w", c, err)
	}
	// A stall or a takeover decides the exit code, whatever error the cancelled work ended with: the Job retries a stall (75) and
	// reads a takeover as a failure (1).
	switch {
	case errors.Is(cause, backup.ErrStalled):
		cause = backup.Fail(backup.ExitTemporary, backup.CodeStalled, cause)
	case errors.Is(cause, backup.ErrSuperseded):
		cause = backup.Fail(backup.ExitFailed, backup.CodeSuperseded, cause)
	}
	if errors.Is(cause, backup.ErrSuperseded) {
		// The row was abandoned: it is not ours to write any more.
		r.d.Log.Error("backup run was superseded", "backup", r.id, "err", cause)
		return nil, classify(cause)
	}
	if ctx.Err() != nil && !errors.Is(cause, backup.ErrStalled) {
		// SIGTERM: a drain, an eviction or a deleted pod. Not a failure for alerting (R-BAK-012).
		_ = r.d.Store.Finish(cctx, r.runID, backup.StateInterrupted, nil, mustJSON(map[string]string{"code": backup.CodeInterrupted, "message": "interrupted by a termination signal"}))
		r.d.Log.Warn("backup run interrupted", "backup", r.id)
		return nil, backup.Fail(backup.ExitTemporary, backup.CodeInterrupted, fmt.Errorf("interrupted: %w", context.Cause(ctx)))
	}
	err = classify(cause)
	code := backup.ErrorCode(err)
	if ferr := r.d.Store.Finish(cctx, r.runID, backup.StateFailed, nil, mustJSON(map[string]any{"code": code, "message": err.Error(), "exitCode": backup.ExitCode(err)})); ferr != nil {
		r.d.Log.Error("could not record the failed run", "err", ferr)
	}
	r.event(cctx, audit.Event{
		Type: audit.EventBackupFailed, Actor: actor(), Outcome: audit.Failure, Reason: code,
		Details: mustJSON(map[string]string{"backupId": string(r.id), "code": code}),
	})
	r.d.Log.Error("backup run failed", "backup", r.id, "code", code, "err", err)
	return nil, err
}
