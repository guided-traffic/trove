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
}

// Result is the outcome of an enqueue.
type Result struct {
	Job Job
	// Created is false when a conflicting job with the same key already
	// exists and Job is that job. The API answers 409 with it.
	Created bool
}

func (qu *Queue) params(e EnqueueSpec) (q.InsertJobParams, KindSpec, error) {
	spec, ok := qu.reg.Kind(e.Kind)
	if !ok {
		return q.InsertJobParams{}, spec, fmt.Errorf("%w: %q", ErrUnknownKind, e.Kind)
	}
	args := []byte("{}")
	if e.Args != nil {
		b, err := json.Marshal(e.Args)
		if err != nil {
			return q.InsertJobParams{}, spec, fmt.Errorf("jobs: args of %q: %w", e.Kind, err)
		}
		args = b
	}
	key := e.DedupKey
	if spec.DedupMode != NoDedup && key == "" {
		key = e.Kind
	}
	if spec.DedupMode == NoDedup && key != "" {
		return q.InsertJobParams{}, spec, kindError(e.Kind, "has no dedup mode, so an enqueue cannot carry a key")
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
	}, spec, nil
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
	res, err := qu.insert(ctx, q.New(ptx), p, DedupMode(p.DedupMode))
	if err != nil {
		return Result{}, err
	}
	if res.Created && qu.sig != nil {
		qu.sig.Notify(tx, "jobs", spec.Queue)
	}
	return res, nil
}

// EnqueueNow inserts a job in a transaction of its own, for callers that have no
// change to commit with it.
func (qu *Queue) EnqueueNow(ctx context.Context, e EnqueueSpec) (Result, error) {
	p, spec, err := qu.params(e)
	if err != nil {
		return Result{}, err
	}
	res, err := qu.insert(ctx, q.New(qu.db), p, DedupMode(p.DedupMode))
	if err != nil {
		return Result{}, err
	}
	if res.Created && qu.sig != nil {
		qu.sig.Publish("jobs", spec.Queue)
	}
	return res, nil
}

// insert runs the insert and resolves a conflict on the dedup key to the
// existing job. A job that ended between the refusal and the lookup lets the
// insert run again.
func (qu *Queue) insert(ctx context.Context, qs *q.Queries, p q.InsertJobParams, mode DedupMode) (Result, error) {
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
			existing, err = qs.FindWaitingJobByKey(ctx, p.DedupKey)
		}
		if err != nil {
			return Result{}, err
		}
		if len(existing) == 1 {
			return Result{Job: fromRow(existing[0])}, nil
		}
	}
	return Result{}, errors.New("jobs: the dedup key kept conflicting with a job that kept ending")
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
