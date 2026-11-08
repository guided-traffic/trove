package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// Signals is the part of the cluster's signal path the queue uses: a wake-up
// after an enqueue commits, and the worker's subscription. *cluster.Signals
// implements it.
type Signals interface {
	Notify(tx meta.Tx, area, payload string)
	Publish(area, payload string)
}

// Queue enqueues jobs. Every role has one; only a [Worker] claims.
type Queue struct {
	db  q.DBTX
	reg *Registry
	sig Signals
	log *slog.Logger
}

// QueueOptions configure [NewQueue].
type QueueOptions struct {
	// DB runs enqueues outside a caller's transaction and the reads: the main pool.
	DB q.DBTX
	// Registry defaults to [Default].
	Registry *Registry
	// Signals wakes the workers after an enqueue commits; nil means workers
	// find the job by polling (jobs.poll_interval).
	Signals Signals
	Log     *slog.Logger
}

// NewQueue creates a queue.
func NewQueue(o QueueOptions) *Queue {
	if o.Registry == nil {
		o.Registry = Default
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	return &Queue{db: o.DB, reg: o.Registry, sig: o.Signals, log: o.Log}
}

// EnqueueSpec describes one job to enqueue.
type EnqueueSpec struct {
	Kind string
	// Args is any JSON-encodable value; nil becomes {}.
	Args any
	// DedupKey is the job key. Empty uses the kind's name when the kind has a
	// dedup mode; a key on a kind without a mode is an error.
	DedupKey string
	// Delay postpones the first attempt, measured on the database's clock.
	Delay time.Duration
	// Priority overrides the kind's; zero keeps it. Lower runs first.
	Priority int
	// TargetKind and TargetKey name the object an API operation acts on (R-API-015).
	TargetKind, TargetKey string
	// CreatedBy is the principal's ID (a UUID); empty for system and scheduled runs.
	CreatedBy string
	// Traceparent is the W3C trace context of the enqueuing request or job. It
	// holds trace context only, never a credential.
	Traceparent string
	// MergeArgs, for a kind in the Coalesce mode only, is called with the arguments of the job that waits under the key when this
	// enqueue finds one, and returns the arguments the waiting job gets and whether they differ: the way a request for more work (a
	// full rebuild where an incremental one waits) upgrades the waiting job instead of queuing a second (Q-ARCH-384). It is not
	// called for a job that is running, and not for a waiting job of another kind. A kind in another mode refuses it.
	MergeArgs func(waiting json.RawMessage) (merged json.RawMessage, changed bool)
}

// Result is the outcome of an enqueue.
type Result struct {
	Job Job
	// Created is false when a conflicting job with the same key already
	// exists and Job is that job. The API answers 409 with it.
	Created bool
	// Moved is true when the job is the waiting job of a Coalesce key and this enqueue moved its run time earlier: the work was
	// asked for sooner than the waiting job would have run (Q-ARCH-384). Job carries the new run time.
	Moved bool
	// Merged is true when the waiting job's arguments were replaced by EnqueueSpec.MergeArgs.
	Merged bool
}

func (qu *Queue) params(e EnqueueSpec) (q.InsertJobParams, KindSpec, error) {
	spec, ok := qu.reg.Kind(e.Kind)
	if !ok {
		return q.InsertJobParams{}, spec, fmt.Errorf("%w: %q", ErrUnknownKind, e.Kind)
	}
	p, err := paramsFor(spec, e)
	return p, spec, err
}

// paramsFor is the row of a job of the given kind spec: the one place that turns a spec and an enqueue request into the
// insert, whether the spec comes from the registry or from the caller.
func paramsFor(spec KindSpec, e EnqueueSpec) (q.InsertJobParams, error) {
	args := []byte("{}")
	if e.Args != nil {
		b, err := json.Marshal(e.Args)
		if err != nil {
			return q.InsertJobParams{}, fmt.Errorf("jobs: args of %q: %w", e.Kind, err)
		}
		args = b
	}
	key := e.DedupKey
	if spec.DedupMode != NoDedup && key == "" {
		key = e.Kind
	}
	if spec.DedupMode == NoDedup && key != "" {
		return q.InsertJobParams{}, kindError(e.Kind, "has no dedup mode, so an enqueue cannot carry a key")
	}
	if e.MergeArgs != nil && spec.DedupMode != Coalesce {
		return q.InsertJobParams{}, kindError(e.Kind, "is not a coalesce kind, so an enqueue cannot merge into a waiting job")
	}
	prio := spec.Priority
	if e.Priority != 0 {
		prio = e.Priority
	}
	return q.InsertJobParams{
		ID: newID(), Kind: e.Kind, Queue: spec.Queue, Args: string(args), Priority: int16(prio), //nolint:gosec // small
		DelaySecs: e.Delay.Seconds(), MaxAttempts: int32(spec.MaxAttempts), //nolint:gosec // small
		DedupKey: key, DedupMode: string(spec.DedupMode),
		TargetKind: e.TargetKind, TargetKey: e.TargetKey, CreatedBy: e.CreatedBy, Traceparent: e.Traceparent,
	}, nil
}

// EnqueueTx inserts a job of the given kind spec in the caller's transaction, as [Queue.Enqueue] does, without the registry
// lookup and without the wake-up: the workers find the row by polling (jobs.poll_interval). It is for a caller that holds no
// queue, such as a replay handler of the restore commands, which build no job registry (Q-HA-180). The spec is the one the
// application registers the kind with, minus its handler, so the row equals the one Enqueue writes; its unset members take
// the defaults of RegisterKind. A conflicting key returns the existing job with Created false.
func EnqueueTx(ctx context.Context, tx meta.Tx, spec KindSpec, e EnqueueSpec) (Result, error) {
	ptx, ok := postgres.Unwrap(tx)
	if !ok {
		return Result{}, errors.New("jobs: EnqueueTx needs a transaction of internal/meta/postgres")
	}
	spec = normalize(spec)
	switch spec.DedupMode {
	case NoDedup, Exclusive, Coalesce:
	default:
		return Result{}, kindError(e.Kind, "unknown dedup mode %q", spec.DedupMode)
	}
	if spec.MaxAttempts < 1 {
		return Result{}, kindError(e.Kind, "MaxAttempts %d", spec.MaxAttempts)
	}
	p, err := paramsFor(spec, e)
	if err != nil {
		return Result{}, err
	}
	return insertJob(ctx, q.New(ptx), p, e)
}

// Enqueue inserts a job in the caller's transaction, so the job exists exactly
// if the change that causes it commits (R-ARCH-016). After the commit the
// workers are woken through the signal area `jobs`; a rollback drops the
// wake-up. A conflicting key returns the existing job with Created false.
func (qu *Queue) Enqueue(ctx context.Context, tx meta.Tx, e EnqueueSpec) (Result, error) {
	ptx, ok := postgres.Unwrap(tx)
	if !ok {
		return Result{}, errors.New("jobs: Enqueue needs a transaction of internal/meta/postgres")
	}
	p, spec, err := qu.params(e)
	if err != nil {
		return Result{}, err
	}
	res, err := insertJob(ctx, q.New(ptx), p, e)
	if err != nil {
		return Result{}, err
	}
	if (res.Created || res.Moved) && qu.sig != nil {
		qu.sig.Notify(tx, "jobs", spec.Queue)
	}
	return res, nil
}

// Running returns the job that runs under a dedup key, read in the caller's transaction; ok is false when none runs. A caller that
// must tell a running job of a coalesce key from the one that waits behind it asks here (Q-ARCH-384): Enqueue reports the waiting one.
func (qu *Queue) Running(ctx context.Context, tx meta.Tx, key string) (j Job, ok bool, err error) {
	ptx, isPG := postgres.Unwrap(tx)
	if !isPG {
		return Job{}, false, errors.New("jobs: Running needs a transaction of internal/meta/postgres")
	}
	rows, err := q.New(ptx).FindRunningJobByKey(ctx, key)
	if err != nil || len(rows) == 0 {
		return Job{}, false, err
	}
	return fromRow(rows[0]), true, nil
}

// EnqueueNow inserts a job in a transaction of its own, for callers that have no
// change to commit with it.
func (qu *Queue) EnqueueNow(ctx context.Context, e EnqueueSpec) (Result, error) {
	p, spec, err := qu.params(e)
	if err != nil {
		return Result{}, err
	}
	res, err := insertJob(ctx, q.New(qu.db), p, e)
	if err != nil {
		return Result{}, err
	}
	if (res.Created || res.Moved) && qu.sig != nil {
		qu.sig.Publish("jobs", spec.Queue)
	}
	return res, nil
}

// insertJob runs the insert and resolves a conflict on the dedup key to the existing job. A job that ended between the refusal and
// the lookup lets the insert run again.
//
// A conflict in the Exclusive mode returns the active job unchanged. A conflict in the Coalesce mode finds the waiting job, locked,
// and adjusts it (Q-ARCH-384): an enqueue that asks for an earlier run time moves the job there, and an enqueue with MergeArgs
// replaces the job's arguments with the merged ones. Nothing is ever moved later, and a job that is running is never touched: the
// work that arrives during a run waits as the one job behind it.
func insertJob(ctx context.Context, qs *q.Queries, p q.InsertJobParams, e EnqueueSpec) (Result, error) {
	mode := DedupMode(p.DedupMode)
	for range 3 {
		rows, err := qs.InsertJob(ctx, p)
		if err != nil {
			return Result{}, err
		}
		if len(rows) == 1 {
			return Result{Job: fromRow(rows[0]), Created: true}, nil
		}
		if p.DedupKey == "" {
			return Result{}, errors.New("jobs: the insert was refused without a dedup key")
		}
		var existing []q.Job
		if mode == Exclusive {
			existing, err = qs.FindActiveJobByKey(ctx, p.DedupKey)
		} else {
			existing, err = qs.LockWaitingJobByKey(ctx, p.DedupKey)
		}
		if err != nil {
			return Result{}, err
		}
		if len(existing) == 1 {
			if mode == Coalesce {
				return adjustWaiting(ctx, qs, existing[0], p, e)
			}
			return Result{Job: fromRow(existing[0])}, nil
		}
	}
	return Result{}, errors.New("jobs: the dedup key kept conflicting with a job that kept ending")
}

// adjustWaiting moves the waiting job of a coalesce key to the run time of the enqueue when that is earlier, and merges the
// enqueue's arguments into it.
func adjustWaiting(ctx context.Context, qs *q.Queries, waiting q.Job, p q.InsertJobParams, e EnqueueSpec) (Result, error) {
	old := fromRow(waiting)
	args, merged := json.RawMessage(nil), false
	if e.MergeArgs != nil && old.Kind == p.Kind {
		args, merged = e.MergeArgs(old.Args)
	}
	rows, err := qs.AdjustWaitingJob(ctx, q.AdjustWaitingJobParams{ID: old.ID, DelaySecs: p.DelaySecs, SetArgs: merged, Args: string(args)})
	if err != nil {
		return Result{}, err
	}
	if len(rows) == 0 {
		return Result{Job: old}, nil
	}
	job := fromRow(rows[0])
	return Result{Job: job, Moved: job.RunAt.Before(old.RunAt), Merged: merged}, nil
}

// Get returns a job; ok is false if it does not exist.
func (qu *Queue) Get(ctx context.Context, id string) (j Job, ok bool, err error) {
	rows, err := q.New(qu.db).GetJob(ctx, id)
	if err != nil || len(rows) == 0 {
		return Job{}, false, err
	}
	return fromRow(rows[0]), true, nil
}

// Cancel cancels a job: a queued job ends cancelled at once, a running one
// becomes cancelling and its handler ends it at the next checkpoint
// (R-ARCH-016). ok is false if the job is not queued or running.
func (qu *Queue) Cancel(ctx context.Context, id string) (j Job, ok bool, err error) {
	rows, err := q.New(qu.db).CancelJob(ctx, id)
	if err != nil || len(rows) == 0 {
		return Job{}, false, err
	}
	return fromRow(rows[0]), true, nil
}
