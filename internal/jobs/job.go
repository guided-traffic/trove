package jobs

import (
	"encoding/json"
	"time"

	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// Job states (the job_state type of the schema).
const (
	StateQueued     = "queued"
	StateRunning    = "running"
	StateSucceeded  = "succeeded"
	StateFailed     = "failed"
	StateCancelling = "cancelling"
	StateCancelled  = "cancelled"
)

// Job is a row of the job table.
type Job struct {
	ID    string
	Kind  string
	Queue string
	Args  json.RawMessage
	State string

	Priority    int
	RunAt       time.Time
	Attempt     int
	MaxAttempts int

	DedupKey     string
	DedupMode    DedupMode
	Task         string
	ScheduledFor time.Time
	TargetKind   string
	TargetKey    string

	LockedBy    string
	LockedUntil time.Time

	ProgressDone  int64
	ProgressTotal int64
	ProgressUnit  string

	Checkpoint json.RawMessage
	Result     json.RawMessage
	Error      json.RawMessage

	CreatedBy   string
	Traceparent string
	CreatedAt   time.Time
	StartedAt   time.Time
	FinishedAt  time.Time
}

// Terminal reports whether the job has ended.
func (j Job) Terminal() bool {
	return j.State == StateSucceeded || j.State == StateFailed || j.State == StateCancelled
}

func fromRow(r q.Job) Job {
	j := Job{
		ID: r.ID.String(), Kind: r.Kind, Queue: r.Queue, Args: r.Args, State: r.State,
		Priority: int(r.Priority), RunAt: r.RunAt.Time, Attempt: int(r.Attempt), MaxAttempts: int(r.MaxAttempts),
		DedupKey: r.DedupKey.String, DedupMode: DedupMode(r.DedupMode.String), Task: r.Task.String,
		ScheduledFor: r.ScheduledFor.Time, TargetKind: r.TargetKind.String, TargetKey: r.TargetKey.String,
		LockedUntil:  r.LockedUntil.Time,
		ProgressDone: r.ProgressDone.Int64, ProgressTotal: r.ProgressTotal.Int64, ProgressUnit: r.ProgressUnit.String,
		Checkpoint: r.Checkpoint, Result: r.Result, Error: r.Error,
		Traceparent: r.Traceparent.String, CreatedAt: r.CreatedAt.Time, StartedAt: r.StartedAt.Time, FinishedAt: r.FinishedAt.Time,
	}
	if r.LockedBy.Valid {
		j.LockedBy = r.LockedBy.String()
	}
	if r.CreatedBy.Valid {
		j.CreatedBy = r.CreatedBy.String()
	}
	return j
}
