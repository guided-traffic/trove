package backup

import (
	"encoding/json"
	"time"
)

// Operation is what a run does (R-BAK-009). create and delete are the target-writing operations, of which one is active
// at a time (R-BAK-010).
type Operation string

// The operations.
const (
	OpCreate Operation = "create"
	OpDelete Operation = "delete"
	OpVerify Operation = "verify"
)

// State is the state of a run (R-BAK-009).
type State string

// The states, in the order a create run passes through them.
const (
	StateStarting             State = "starting"
	StateSnapshot             State = "snapshot"
	StateCopying              State = "copying"
	StateFinalizing           State = "finalizing"
	StatePruning              State = "pruning"
	StateCompleted            State = "completed"
	StateCompletedWithWarning State = "completed_with_warnings"
	StateFailed               State = "failed"
	StateInterrupted          State = "interrupted"
	StateAbandoned            State = "abandoned"
)

// Active reports whether a run in the state is still running, the states of the partial unique index
// backup_run_one_active.
func (s State) Active() bool {
	switch s {
	case StateStarting, StateSnapshot, StateCopying, StateFinalizing, StatePruning:
		return true
	}
	return false
}

// Succeeded reports whether the state is one of the two successful end states.
func (s State) Succeeded() bool { return s == StateCompleted || s == StateCompletedWithWarning }

// Hold is what a run keeps from garbage collection (R-BAK-007).
type Hold string

// The holds.
const (
	// HoldNone has no effect on garbage collection.
	HoldNone Hold = "none"
	// HoldAll lets no blob move to deleting, from the shared migration lock until the pins are committed.
	HoldAll Hold = "all"
	// HoldPinned keeps the pinned digests, until the last pin is removed.
	HoldPinned Hold = "pinned"
)

// Trigger says who started a run.
type Trigger string

// The triggers.
const (
	TriggerSchedule Trigger = "schedule"
	TriggerManual   Trigger = "manual"
)

// Progress is the run's progress object (R-BAK-009, R-API-050). The heartbeat writes it; the gauges and the API read it.
type Progress struct {
	Phase       string `json:"phase,omitempty"`
	TablesDone  int    `json:"tablesDone,omitempty"`
	TablesTotal int    `json:"tablesTotal,omitempty"`
	BlobsToCopy int64  `json:"blobsToCopy,omitempty"`
	BlobsCopied int64  `json:"blobsCopied,omitempty"`
	BytesCopied int64  `json:"bytesCopied,omitempty"`
	// PinsLeft is the number of pins not yet removed, the value of trove_backup_pinned_blobs. No query counts
	// backup_blob_pin for the gauges (R-BAK-014).
	PinsLeft int64 `json:"pinsLeft,omitempty"`
	// SnapshotOpenSince is when the snapshot transaction began; nil while no snapshot is open.
	SnapshotOpenSince *time.Time `json:"snapshotOpenSince,omitempty"`
}

// Run is one row of backup_run.
type Run struct {
	ID             string
	Operation      Operation
	BackupID       string
	State          State
	Hold           Hold
	Trigger        Trigger
	Target         string
	Pod            string
	TroveVersion   string
	SnapshotAt     *time.Time
	Progress       Progress
	Result         json.RawMessage
	Error          json.RawMessage
	StartedAt      time.Time
	HeartbeatAt    time.Time
	LeaseExpiresAt time.Time
	FinishedAt     *time.Time
}

// LeaseExpired is true for a run in an active state whose lease has passed at now (R-BAK-010). The caller passes the
// database's clock where it has one.
func (r Run) LeaseExpired(now time.Time) bool {
	return r.State.Active() && r.LeaseExpiresAt.Before(now)
}

// NewRun is what Begin inserts.
type NewRun struct {
	ID           string
	Operation    Operation
	BackupID     string
	Trigger      Trigger
	Target       string
	Pod          string
	TroveVersion string
	Lease        time.Duration
}
